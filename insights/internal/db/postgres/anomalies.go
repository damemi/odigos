package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// upsertAnomalyIssueSQL merges an occurrence or a decision into its issue row:
// counts add up, score/last_seen/decisions keep the max, first_seen keeps the
// min, and the descriptive columns follow the newest occurrence. A decision row
// carries last_seen = epoch, so it never replaces the descriptive columns.
const upsertAnomalyIssueSQL = `INSERT INTO anomaly_issues AS a
	(transaction_id, signature, triggered_classes, offending, evidence, policy_id,
	 occurrences, max_score, first_seen, last_seen, accepted_at, dismissed_at, reopened_at, last_trace_id)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	ON CONFLICT (transaction_id, signature) DO UPDATE SET
		triggered_classes = CASE WHEN EXCLUDED.last_seen >= a.last_seen THEN EXCLUDED.triggered_classes ELSE a.triggered_classes END,
		offending         = CASE WHEN EXCLUDED.last_seen >= a.last_seen THEN EXCLUDED.offending ELSE a.offending END,
		evidence          = CASE WHEN EXCLUDED.last_seen >= a.last_seen THEN EXCLUDED.evidence ELSE a.evidence END,
		policy_id         = CASE WHEN EXCLUDED.last_seen >= a.last_seen THEN EXCLUDED.policy_id ELSE a.policy_id END,
		last_trace_id     = CASE WHEN EXCLUDED.last_seen >= a.last_seen THEN EXCLUDED.last_trace_id ELSE a.last_trace_id END,
		occurrences       = a.occurrences + EXCLUDED.occurrences,
		max_score         = GREATEST(a.max_score, EXCLUDED.max_score),
		first_seen        = LEAST(a.first_seen, EXCLUDED.first_seen),
		last_seen         = GREATEST(a.last_seen, EXCLUDED.last_seen),
		accepted_at       = GREATEST(a.accepted_at, EXCLUDED.accepted_at),
		dismissed_at      = GREATEST(a.dismissed_at, EXCLUDED.dismissed_at),
		reopened_at       = GREATEST(a.reopened_at, EXCLUDED.reopened_at)`

// RecordAnomalyOccurrences merges one occurrence group per flushed issue.
func (r *Repo) RecordAnomalyOccurrences(ctx context.Context, occs []repo.AnomalyOccurrence) error {
	if len(occs) == 0 {
		return nil
	}
	sorted := append([]repo.AnomalyOccurrence(nil), occs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TransactionID != sorted[j].TransactionID {
			return sorted[i].TransactionID < sorted[j].TransactionID
		}
		return sorted[i].Signature < sorted[j].Signature
	})
	rows := make([][]any, 0, len(sorted))
	for _, o := range sorted {
		triggered, _ := json.Marshal(o.TriggeredClasses)
		evidence := o.Evidence
		if len(evidence) == 0 {
			evidence = json.RawMessage(`{}`)
		}
		firstSeen, lastSeen := o.FirstSeen, o.LastSeen
		if lastSeen.IsZero() {
			lastSeen = time.Now()
		}
		if firstSeen.IsZero() {
			firstSeen = lastSeen
		}
		count := o.Occurrences
		if count <= 0 {
			count = 1
		}
		rows = append(rows, []any{
			o.TransactionID, o.Signature, string(triggered), o.Offending, string(evidence), o.PolicyID,
			int64(count), int32(o.MaxScore), firstSeen, lastSeen, epoch, epoch, epoch, o.LastTraceID,
		})
	}
	if err := execBatch(ctx, r.pool, upsertAnomalyIssueSQL, rows); err != nil {
		return fmt.Errorf("record anomaly occurrences: %w", err)
	}
	return nil
}

// ResolveAnomalyIssue stamps the chosen resolution's timestamp so the newest
// decision wins as the issue status.
func (r *Repo) ResolveAnomalyIssue(ctx context.Context, transactionID int64, signature string, resolution repo.AnomalyResolution) error {
	acceptedAt, dismissedAt, reopenedAt := epoch, epoch, epoch
	switch resolution {
	case repo.AnomalyResolveAccept:
		acceptedAt = time.Now()
	case repo.AnomalyResolveReopen:
		reopenedAt = time.Now()
	default:
		dismissedAt = time.Now()
	}
	if _, err := r.pool.Exec(ctx, upsertAnomalyIssueSQL,
		transactionID, signature, "", "", "{}", int64(0),
		int64(0), int32(0), firstSeenFuture, epoch, acceptedAt, dismissedAt, reopenedAt, "",
	); err != nil {
		return fmt.Errorf("resolve anomaly issue %d/%s: %w", transactionID, signature, err)
	}
	return nil
}

// anomalyIssueLeanCols is scanAnomalyIssueLean's column order (no evidence).
const anomalyIssueLeanCols = `a.transaction_id, a.signature, a.triggered_classes, a.offending, a.policy_id,
	a.occurrences, a.max_score, a.first_seen, a.last_seen, a.accepted_at, a.dismissed_at, a.reopened_at, a.last_trace_id,
	COALESCE(t.service, ''), COALESCE(t.namespace, ''), COALESCE(t.operation, ''), COALESCE(t.kind, '')`

// anomalyIssueCols is scanAnomalyIssue's column order (includes evidence).
const anomalyIssueCols = `a.transaction_id, a.signature, a.triggered_classes, a.offending, a.evidence, a.policy_id,
	a.occurrences, a.max_score, a.first_seen, a.last_seen, a.accepted_at, a.dismissed_at, a.reopened_at, a.last_trace_id,
	COALESCE(t.service, ''), COALESCE(t.namespace, ''), COALESCE(t.operation, ''), COALESCE(t.kind, '')`

// ListAnomalyIssues returns issue summaries whose newest timestamp is within
// the window, worst/most-recent first. Evidence is not loaded - fetch with
// GetAnomalyIssue for the investigate view.
func (r *Repo) ListAnomalyIssues(ctx context.Context, f repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	var (
		where strings.Builder
		a     args
	)
	appendActivityWindowFilter(&where, &a, f, "a")
	appendTxnIdentityFilter(&where, &a, f, "t")
	appendAnomalyStatusFilter(&where, f.Status)

	q := fmt.Sprintf(`SELECT %s
		 FROM anomaly_issues AS a
		 LEFT JOIN transactions AS t ON t.transaction_id = a.transaction_id
		 %s
		 ORDER BY a.max_score DESC, a.last_seen DESC
		 LIMIT %s`, anomalyIssueLeanCols, where.String(), a.add(limit))

	return queryRows(ctx, r.pool, "list anomaly issues", q, scanAnomalyIssueLean, a...)
}

// ListAnomalyIssuesByTransaction returns one transaction's issue summaries
// within the window, worst/most-recent first. Evidence is not loaded.
func (r *Repo) ListAnomalyIssuesByTransaction(ctx context.Context, transactionID int64, f repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	var (
		where strings.Builder
		a     args
	)
	appendActivityWindowFilter(&where, &a, f, "a")
	where.WriteString(" AND a.transaction_id = " + a.add(transactionID))
	appendAnomalyStatusFilter(&where, f.Status)

	q := fmt.Sprintf(`SELECT %s
		 FROM anomaly_issues AS a
		 LEFT JOIN transactions AS t ON t.transaction_id = a.transaction_id
		 %s
		 ORDER BY a.max_score DESC, a.last_seen DESC
		 LIMIT %s`, anomalyIssueLeanCols, where.String(), a.add(limit))

	return queryRows(ctx, r.pool, "list anomaly issues by transaction", q, scanAnomalyIssueLean, a...)
}

// GetAnomalyIssue returns one issue by (transaction, signature) including
// evidence, ok=false when unknown.
func (r *Repo) GetAnomalyIssue(ctx context.Context, transactionID int64, signature string) (repo.AnomalyIssue, bool, error) {
	return queryRow(ctx, r.pool, "get anomaly issue",
		`SELECT `+anomalyIssueCols+`
		 FROM anomaly_issues AS a
		 LEFT JOIN transactions AS t ON t.transaction_id = a.transaction_id
		 WHERE a.transaction_id = $1 AND a.signature = $2`,
		scanAnomalyIssue, transactionID, signature)
}

// DeleteAnomalyIssues removes every anomaly issue row for a transaction. Cold
// path: called on a baseline re-learn so findings scored against the wiped
// baseline are dropped instead of contradicting the fresh one. If the behavior
// is still anomalous it re-fires against the new baseline as a fresh issue.
func (r *Repo) DeleteAnomalyIssues(ctx context.Context, transactionID int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM anomaly_issues WHERE transaction_id = $1`, transactionID); err != nil {
		return fmt.Errorf("delete anomaly issues %d: %w", transactionID, err)
	}
	return nil
}

func scanAnomalyIssueLean(s rowScanner) (repo.AnomalyIssue, error) {
	var (
		issue                               repo.AnomalyIssue
		triggered, kind                     string
		occ                                 int64
		maxScore                            int32
		firstSeen, lastSeen                 time.Time
		acceptedAt, dismissedAt, reopenedAt time.Time
	)
	if err := s.Scan(
		&issue.TransactionID, &issue.Signature, &triggered, &issue.Offending, &issue.PolicyID,
		&occ, &maxScore, &firstSeen, &lastSeen, &acceptedAt, &dismissedAt, &reopenedAt, &issue.LastTraceID,
		&issue.Service, &issue.Namespace, &issue.Operation, &kind,
	); err != nil {
		return repo.AnomalyIssue{}, err
	}
	fillAnomalyIssueMeta(&issue, triggered, kind, occ, maxScore, firstSeen, lastSeen, acceptedAt, dismissedAt, reopenedAt)
	return issue, nil
}

func scanAnomalyIssue(s rowScanner) (repo.AnomalyIssue, error) {
	var (
		issue                               repo.AnomalyIssue
		triggered, evidence, kind           string
		occ                                 int64
		maxScore                            int32
		firstSeen, lastSeen                 time.Time
		acceptedAt, dismissedAt, reopenedAt time.Time
	)
	if err := s.Scan(
		&issue.TransactionID, &issue.Signature, &triggered, &issue.Offending, &evidence, &issue.PolicyID,
		&occ, &maxScore, &firstSeen, &lastSeen, &acceptedAt, &dismissedAt, &reopenedAt, &issue.LastTraceID,
		&issue.Service, &issue.Namespace, &issue.Operation, &kind,
	); err != nil {
		return repo.AnomalyIssue{}, err
	}
	fillAnomalyIssueMeta(&issue, triggered, kind, occ, maxScore, firstSeen, lastSeen, acceptedAt, dismissedAt, reopenedAt)
	issue.Evidence = json.RawMessage(evidence)
	return issue, nil
}

func fillAnomalyIssueMeta(
	issue *repo.AnomalyIssue,
	triggered, kind string,
	occ int64, maxScore int32,
	firstSeen, lastSeen, acceptedAt, dismissedAt, reopenedAt time.Time,
) {
	if triggered != "" {
		var classes []repo.DeviationClass
		if err := json.Unmarshal([]byte(triggered), &classes); err == nil {
			issue.TriggeredClasses = classes
		}
	}
	issue.Kind = repo.TransactionKind(kind)
	issue.Occurrences = occ
	issue.MaxScore = int(maxScore)
	issue.FirstSeen = firstSeen
	issue.LastSeen = lastSeen
	issue.Status = anomalyIssueStatus(acceptedAt, dismissedAt, reopenedAt)
}

// anomalyIssueStatus picks the lifecycle bucket from the latest decision timestamp, accept winning ties.
func anomalyIssueStatus(acceptedAt, dismissedAt, reopenedAt time.Time) repo.AnomalyIssueStatus {
	switch terminal, dismissed := stickyDecisionStatus(acceptedAt, dismissedAt, reopenedAt); {
	case terminal:
		return repo.AnomalyIssueAccepted
	case dismissed:
		return repo.AnomalyIssueDismissed
	default:
		return repo.AnomalyIssueOpen
	}
}
