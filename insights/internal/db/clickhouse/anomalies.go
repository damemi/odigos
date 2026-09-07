package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// RecordAnomalyOccurrences appends one occurrence-counter row per flushed group for the AggregatingMergeTree to sum.
func (r *Repo) RecordAnomalyOccurrences(ctx context.Context, occs []repo.AnomalyOccurrence) error {
	if len(occs) == 0 {
		return nil
	}
	err := r.insert(ctx,
		`INSERT INTO anomaly_issues
			(transaction_id, signature, triggered_classes, offending, evidence, policy_id,
			 occurrences, max_score, first_seen, last_seen, accepted_at, dismissed_at, reopened_at, last_trace_id)`,
		func(batch driver.Batch) error {
			for _, o := range occs {
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
				if err := batch.Append(
					o.TransactionID, o.Signature, string(triggered), o.Offending, string(evidence), o.PolicyID,
					uint64(count), int32(o.MaxScore), firstSeen, lastSeen, decisionEpoch, decisionEpoch, decisionEpoch, o.LastTraceID,
				); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("record anomaly occurrences: %w", err)
	}
	return nil
}

// ResolveAnomalyIssue appends a decision row stamping the chosen resolution's timestamp so the newest decision max-merges as the issue status.
func (r *Repo) ResolveAnomalyIssue(ctx context.Context, transactionID int64, signature string, resolution repo.AnomalyResolution) error {
	acceptedAt, dismissedAt, reopenedAt := decisionEpoch, decisionEpoch, decisionEpoch
	switch resolution {
	case repo.AnomalyResolveAccept:
		acceptedAt = time.Now()
	case repo.AnomalyResolveReopen:
		reopenedAt = time.Now()
	default:
		dismissedAt = time.Now()
	}
	err := r.insert(ctx,
		`INSERT INTO anomaly_issues
			(transaction_id, signature, triggered_classes, offending, evidence, policy_id,
			 occurrences, max_score, first_seen, last_seen, accepted_at, dismissed_at, reopened_at, last_trace_id)`,
		func(batch driver.Batch) error {
			return batch.Append(
				transactionID, signature, "", "", "{}", int64(0),
				uint64(0), int32(0), firstSeenFuture, decisionEpoch, acceptedAt, dismissedAt, reopenedAt, "",
			)
		})
	if err != nil {
		return fmt.Errorf("resolve anomaly issue %d/%s: %w", transactionID, signature, err)
	}
	return nil
}

// anomalyIssueAggLean folds AggregatingMergeTree rows per (transaction,
// signature) WITHOUT FINAL and WITHOUT the heavy evidence blob. List/findings
// paths use this so ranking never argMax-materializes per-class JSON.
//
// Aggregated decision timestamps MUST use distinct aliases (newest_seen,
// latest_accepted, …). An alias that shadows a raw column makes ClickHouse
// resolve WHERE/argMax(..., col) to the aggregate (ILLEGAL_AGGREGATION, 184).
const anomalyIssueAggLean = `SELECT
		transaction_id, signature,
		argMax(triggered_classes, last_seen) AS triggered_classes,
		argMax(offending, last_seen)         AS offending,
		argMax(policy_id, last_seen)         AS policy_id,
		sum(occurrences)                     AS occurrences,
		max(max_score)                       AS max_score,
		min(first_seen)                      AS first_seen,
		max(last_seen)                       AS newest_seen,
		max(accepted_at)                     AS latest_accepted,
		max(dismissed_at)                    AS latest_dismissed,
		max(reopened_at)                     AS latest_reopened,
		argMax(last_trace_id, last_seen)     AS last_trace_id
	FROM anomaly_issues`

// anomalyIssueAgg is the detail aggregate: lean columns plus evidence.
const anomalyIssueAgg = `SELECT
		transaction_id, signature,
		argMax(triggered_classes, last_seen) AS triggered_classes,
		argMax(offending, last_seen)         AS offending,
		argMax(evidence, last_seen)          AS evidence,
		argMax(policy_id, last_seen)         AS policy_id,
		sum(occurrences)                     AS occurrences,
		max(max_score)                       AS max_score,
		min(first_seen)                      AS first_seen,
		max(last_seen)                       AS newest_seen,
		max(accepted_at)                     AS latest_accepted,
		max(dismissed_at)                    AS latest_dismissed,
		max(reopened_at)                     AS latest_reopened,
		argMax(last_trace_id, last_seen)     AS last_trace_id
	FROM anomaly_issues`

// anomalyIssueLeanCols is scanAnomalyIssueLean's column order (no evidence).
const anomalyIssueLeanCols = `a.transaction_id, a.signature, a.triggered_classes, a.offending, a.policy_id,
	a.occurrences, a.max_score, a.first_seen, a.newest_seen, a.latest_accepted, a.latest_dismissed, a.latest_reopened, a.last_trace_id,
	t.service, t.namespace, t.operation, t.kind`

// anomalyIssueCols is scanAnomalyIssue's column order (includes evidence).
const anomalyIssueCols = `a.transaction_id, a.signature, a.triggered_classes, a.offending, a.evidence, a.policy_id,
	a.occurrences, a.max_score, a.first_seen, a.newest_seen, a.latest_accepted, a.latest_dismissed, a.latest_reopened, a.last_trace_id,
	t.service, t.namespace, t.operation, t.kind`

// ListAnomalyIssues returns deduped issue summaries whose newest timestamp is
// within the window, worst/most-recent first. Evidence is not loaded - fetch
// with GetAnomalyIssue for the investigate view.
func (r *Repo) ListAnomalyIssues(ctx context.Context, f repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	var where strings.Builder
	var whereArgs []any
	appendActivityWindowFilter(&where, &whereArgs, f,
		`greatest(a.newest_seen, a.latest_accepted, a.latest_dismissed, a.latest_reopened)`)
	appendTxnIdentityFilter(&where, &whereArgs, f, "t")
	appendAnomalyStatusFilter(&where, f.Status)

	inner := anomalyIssueAggLean + ` ` + anomalyWindowWhere + ` GROUP BY transaction_id, signature`
	args := append(sinceArgs(f.Since), whereArgs...)
	args = append(args, uint64(limit))

	q := fmt.Sprintf(`SELECT %s
		 FROM (%s) AS a
		 LEFT JOIN transactions AS t FINAL ON t.transaction_id = a.transaction_id
		 %s
		 ORDER BY a.max_score DESC, a.newest_seen DESC
		 LIMIT ?`, anomalyIssueLeanCols, inner, where.String())

	return queryRows(ctx, r.conn, "list anomaly issues", q, scanAnomalyIssueLean, args...)
}

// ListAnomalyIssuesByTransaction returns one transaction's deduped issue
// summaries within the window, worst/most-recent first. Evidence is not loaded.
func (r *Repo) ListAnomalyIssuesByTransaction(ctx context.Context, transactionID int64, f repo.FindingListFilter) ([]repo.AnomalyIssue, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	var where strings.Builder
	var whereArgs []any
	appendActivityWindowFilter(&where, &whereArgs, f,
		`greatest(a.newest_seen, a.latest_accepted, a.latest_dismissed, a.latest_reopened)`)
	appendAnomalyStatusFilter(&where, f.Status)

	inner := anomalyIssueAggLean + `
		       WHERE transaction_id = ?
		         AND (last_seen >= ? OR accepted_at >= ? OR dismissed_at >= ? OR reopened_at >= ?)
		       GROUP BY transaction_id, signature`
	args := append([]any{transactionID}, sinceArgs(f.Since)...)
	args = append(args, whereArgs...)
	args = append(args, uint64(limit))

	q := fmt.Sprintf(`SELECT %s
		 FROM (%s) AS a
		 LEFT JOIN transactions AS t FINAL ON t.transaction_id = a.transaction_id
		 %s
		 ORDER BY a.max_score DESC, a.newest_seen DESC
		 LIMIT ?`, anomalyIssueLeanCols, inner, where.String())

	return queryRows(ctx, r.conn, "list anomaly issues by transaction", q, scanAnomalyIssueLean, args...)
}

// GetAnomalyIssue returns one issue by (transaction, signature) including
// evidence, ok=false when unknown.
func (r *Repo) GetAnomalyIssue(ctx context.Context, transactionID int64, signature string) (repo.AnomalyIssue, bool, error) {
	return queryRow(ctx, r.conn, "get anomaly issue",
		`SELECT `+anomalyIssueCols+`
		 FROM (`+anomalyIssueAgg+`
		       WHERE transaction_id = ? AND signature = ?
		       GROUP BY transaction_id, signature) AS a
		 LEFT JOIN (SELECT transaction_id, service, namespace, operation, kind
		            FROM transactions FINAL WHERE transaction_id = ?) AS t
		   ON t.transaction_id = a.transaction_id
		 LIMIT 1`,
		scanAnomalyIssue, transactionID, signature, transactionID)
}

// DeleteAnomalyIssues removes every anomaly issue row for a transaction. Cold
// path: called on a baseline re-learn so findings scored against the wiped
// baseline are dropped instead of contradicting the fresh one. Runs as a
// synchronous mutation so a re-fire right after can't race a pending delete. If
// the behaviour is still anomalous it re-fires against the new baseline as a
// fresh issue.
func (r *Repo) DeleteAnomalyIssues(ctx context.Context, transactionID int64) error {
	if err := r.conn.Exec(syncMutationCtx(ctx),
		`ALTER TABLE anomaly_issues DELETE WHERE transaction_id = ?`,
		transactionID); err != nil {
		return fmt.Errorf("delete anomaly issues %d: %w", transactionID, err)
	}
	return nil
}

func scanAnomalyIssueLean(s rowScanner) (repo.AnomalyIssue, error) {
	var (
		issue                               repo.AnomalyIssue
		triggered, kind                     string
		occ                                 uint64
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
		occ                                 uint64
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
	occ uint64, maxScore int32,
	firstSeen, lastSeen, acceptedAt, dismissedAt, reopenedAt time.Time,
) {
	if triggered != "" {
		var classes []repo.DeviationClass
		if err := json.Unmarshal([]byte(triggered), &classes); err == nil {
			issue.TriggeredClasses = classes
		}
	}
	issue.Kind = repo.TransactionKind(kind)
	issue.Occurrences = int64(occ)
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
