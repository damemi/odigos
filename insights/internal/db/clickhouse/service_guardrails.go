package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// ListServiceGuardrails returns every declared guardrail across all scopes.
func (r *Repo) ListServiceGuardrails(ctx context.Context) ([]repo.ServiceGuardrail, error) {
	return queryRows(ctx, r.conn, "list service guardrails",
		`SELECT scope_type, scope_key, rule_lists, rule_modes, rule_origins, rule_fingerprints, rule_correlations, notify_muted, excluded_integration_ids
		 FROM service_guardrails FINAL
		 ORDER BY scope_type ASC, scope_key ASC`,
		scanServiceGuardrail)
}

func scanServiceGuardrail(s rowScanner) (repo.ServiceGuardrail, error) {
	var (
		scopeType        string
		scopeKey         string
		ruleLists        map[string][]string // native Map(String, Array(String))
		ruleModes        map[string]string   // native Map(String, String)
		ruleOrigins      map[string]string
		ruleFingerprints map[string]string
		ruleCorrelations map[string]string // rule_key -> JSON([]CorrelationSpec)
		muted            uint8
		excluded         []string
	)
	if err := s.Scan(&scopeType, &scopeKey, &ruleLists, &ruleModes, &ruleOrigins, &ruleFingerprints, &ruleCorrelations, &muted, &excluded); err != nil {
		return repo.ServiceGuardrail{}, err
	}
	guardrail := repo.ServiceGuardrail{
		Scope:    repo.NormalizePolicyScope(repo.PolicyScope(scopeType)),
		ScopeKey: scopeKey,
		Delivery: scanDelivery(muted, excluded),
	}
	// Retype the column's string keys to the domain's RuleKey; leave the maps nil
	// (not empty) when unset so an unconfigured guardrail stays sparse.
	if len(ruleLists) > 0 {
		guardrail.RuleLists = make(map[repo.RuleKey][]string, len(ruleLists))
		for key, list := range ruleLists {
			guardrail.RuleLists[repo.RuleKey(key)] = list
		}
	}
	if len(ruleModes) > 0 {
		guardrail.RuleModes = make(map[repo.RuleKey]repo.RuleMode, len(ruleModes))
		for key, mode := range ruleModes {
			guardrail.RuleModes[repo.RuleKey(key)] = repo.NormalizeRuleMode(repo.RuleMode(mode))
		}
	}
	if len(ruleOrigins) > 0 {
		guardrail.RuleOrigins = make(map[repo.RuleKey]string, len(ruleOrigins))
		for key, origin := range ruleOrigins {
			guardrail.RuleOrigins[repo.RuleKey(key)] = origin
		}
	}
	if len(ruleFingerprints) > 0 {
		guardrail.RuleFingerprints = make(map[repo.RuleKey]string, len(ruleFingerprints))
		for key, fingerprint := range ruleFingerprints {
			guardrail.RuleFingerprints[repo.RuleKey(key)] = fingerprint
		}
	}
	if len(ruleCorrelations) > 0 {
		guardrail.RuleCorrelations = make(map[repo.RuleKey][]repo.CorrelationSpec, len(ruleCorrelations))
		for key, encoded := range ruleCorrelations {
			if encoded == "" {
				continue
			}
			var specs []repo.CorrelationSpec
			if err := json.Unmarshal([]byte(encoded), &specs); err != nil {
				return repo.ServiceGuardrail{}, fmt.Errorf("decode rule_correlations[%s]: %w", key, err)
			}
			guardrail.RuleCorrelations[repo.RuleKey(key)] = specs
		}
	}
	return guardrail, nil
}

// UpsertServiceGuardrail writes one guardrail, keyed by (scope, scopeKey). The
// table is a ReplacingMergeTree ordered by scope, so re-inserting the same scope
// replaces it.
func (r *Repo) UpsertServiceGuardrail(ctx context.Context, guardrail repo.ServiceGuardrail) error {
	// Narrow the domain's typed keys to plain strings and always pass concrete
	// (possibly empty) maps - the driver rejects a nil Map value.
	ruleLists := make(map[string][]string, len(guardrail.RuleLists))
	for key, list := range guardrail.RuleLists {
		ruleLists[string(key)] = list
	}
	ruleModes := make(map[string]string, len(guardrail.RuleModes))
	for key, mode := range guardrail.RuleModes {
		ruleModes[string(key)] = string(repo.NormalizeRuleMode(mode))
	}
	ruleOrigins := make(map[string]string, len(guardrail.RuleOrigins))
	for key, origin := range guardrail.RuleOrigins {
		if origin == "" {
			continue
		}
		ruleOrigins[string(key)] = origin
	}
	ruleFingerprints := make(map[string]string, len(guardrail.RuleFingerprints))
	for key, fingerprint := range guardrail.RuleFingerprints {
		if fingerprint == "" {
			continue
		}
		ruleFingerprints[string(key)] = fingerprint
	}
	ruleCorrelations := make(map[string]string, len(guardrail.RuleCorrelations))
	for key, specs := range guardrail.RuleCorrelations {
		if len(specs) == 0 {
			continue
		}
		encoded, err := json.Marshal(specs)
		if err != nil {
			return fmt.Errorf("encode rule_correlations[%s]: %w", key, err)
		}
		ruleCorrelations[string(key)] = string(encoded)
	}
	scope := repo.NormalizePolicyScope(guardrail.Scope)
	muted, excluded := deliveryColumns(guardrail.Delivery)
	err := r.insert(ctx,
		"INSERT INTO service_guardrails (scope_type, scope_key, rule_lists, rule_modes, rule_origins, rule_fingerprints, rule_correlations, notify_muted, excluded_integration_ids, updated_at)",
		func(batch driver.Batch) error {
			return batch.Append(string(scope), guardrail.ScopeKey, ruleLists, ruleModes, ruleOrigins, ruleFingerprints, ruleCorrelations, muted, excluded, time.Now())
		})
	if err != nil {
		return fmt.Errorf("upsert service guardrail: %w", err)
	}
	return nil
}

// DeleteServiceGuardrail drops one scoped guardrail via an ALTER ... DELETE
// mutation (ReplacingMergeTree has no real delete); admin action, off the hot path.
func (r *Repo) DeleteServiceGuardrail(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	if err := r.conn.Exec(ctx,
		"ALTER TABLE service_guardrails DELETE WHERE scope_type = ? AND scope_key = ?",
		string(repo.NormalizePolicyScope(scope)), scopeKey); err != nil {
		return fmt.Errorf("delete service guardrail %s/%s: %w", scope, scopeKey, err)
	}
	return nil
}

// RecordGuardrailViolations appends one occurrence-counter row per hit. Pure
// appends (no read) on the buffered-flush path; the AggregatingMergeTree sums
// occurrences, min-merges first_seen, and max-merges score/last_seen per (scope,key,rule,offending). An
// occurrence never touches the decision timestamps (both epoch), so a sticky
// accept/dismiss survives ongoing traffic.
func (r *Repo) RecordGuardrailViolations(ctx context.Context, hits []repo.GuardrailViolationHit) error {
	if len(hits) == 0 {
		return nil
	}
	err := r.insert(ctx,
		`INSERT INTO guardrail_violations
			(scope_type, scope_key, rule_key, offending, occurrences, max_score, first_seen, last_seen, accepted_at, dismissed_at, reopened_at, last_trace_id)`,
		func(batch driver.Batch) error {
			for _, h := range hits {
				lastSeen := h.LastSeen
				if lastSeen.IsZero() {
					lastSeen = time.Now()
				}
				firstSeen := h.FirstSeen
				if firstSeen.IsZero() {
					firstSeen = lastSeen
				}
				occ := h.Occurrences
				if occ <= 0 {
					occ = 1
				}
				if err := batch.Append(
					string(repo.NormalizePolicyScope(h.Scope)), h.ScopeKey, string(h.RuleKey), h.Offending,
					uint64(occ), int32(h.MaxScore), firstSeen, lastSeen, decisionEpoch, decisionEpoch, decisionEpoch, h.LastTraceID,
				); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("record guardrail violations: %w", err)
	}
	return nil
}

// DeleteGuardrailViolationsForRule drops every violation group for one rule key.
// Synchronous mutation (cold path); used on transaction inventory reset.
func (r *Repo) DeleteGuardrailViolationsForRule(ctx context.Context, rule repo.RuleKey) error {
	if err := r.conn.Exec(syncMutationCtx(ctx),
		`ALTER TABLE guardrail_violations DELETE WHERE rule_key = ?`,
		string(rule)); err != nil {
		return fmt.Errorf("delete guardrail violations for rule %s: %w", rule, err)
	}
	return nil
}

// ResolveGuardrailViolation appends a decision row for one group, stamping the
// column for the chosen resolution with now and leaving the others at epoch
// (last_seen epoch, occurrences 0). The max-merge makes the newest decision win.
// Accept/dismiss are sticky (ongoing occurrences bump the count, not the
// decision); reopen stamps a fresh reopened_at that outdates a prior dismiss,
// moving the group back to the open tab for re-triage.
func (r *Repo) ResolveGuardrailViolation(ctx context.Context, scope repo.PolicyScope, scopeKey string, rule repo.RuleKey, offending string, resolution repo.GuardrailResolution) error {
	acceptedAt, dismissedAt, reopenedAt := decisionEpoch, decisionEpoch, decisionEpoch
	switch resolution {
	case repo.GuardrailResolveAccept:
		acceptedAt = time.Now()
	case repo.GuardrailResolveReopen:
		reopenedAt = time.Now()
	default:
		dismissedAt = time.Now()
	}
	err := r.insert(ctx,
		`INSERT INTO guardrail_violations
			(scope_type, scope_key, rule_key, offending, occurrences, max_score, first_seen, last_seen, accepted_at, dismissed_at, reopened_at, last_trace_id)`,
		func(batch driver.Batch) error {
			return batch.Append(
				string(repo.NormalizePolicyScope(scope)), scopeKey, string(rule), offending,
				uint64(0), int32(0), firstSeenFuture, decisionEpoch, acceptedAt, dismissedAt, reopenedAt, "",
			)
		})
	if err != nil {
		return fmt.Errorf("resolve guardrail violation %s/%s/%s: %w", scopeKey, rule, offending, err)
	}
	return nil
}

// guardrailViolationAgg folds AggregatingMergeTree rows per violation group
// WITHOUT FINAL - same pattern as anomalyIssueAggLean.
// Decision timestamps use distinct aliases (newest_seen, latest_accepted, …)
// so a window WHERE on raw columns never resolves to the aggregate alias
// (ILLEGAL_AGGREGATION, error 184) - same pitfall as anomalyIssueAggLean.
const guardrailViolationAgg = `SELECT
		scope_key, rule_key, offending,
		sum(occurrences)                     AS occurrences,
		max(max_score)                       AS max_score,
		min(first_seen)                      AS first_seen,
		max(last_seen)                       AS newest_seen,
		max(accepted_at)                      AS latest_accepted,
		max(dismissed_at)                    AS latest_dismissed,
		max(reopened_at)                     AS latest_reopened,
		argMax(last_trace_id, last_seen)     AS last_trace_id
	FROM guardrail_violations`

// ListGuardrailViolations returns findings over the window with their derived
// status (open/dismissed/accepted), one row per group, worst/most-recent first.
// Explicit GROUP BY replaces FINAL merge-at-read.
func (r *Repo) ListGuardrailViolations(ctx context.Context, f repo.FindingListFilter) ([]repo.GuardrailViolationGroup, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	var where strings.Builder
	var whereArgs []any
	appendActivityWindowFilter(&where, &whereArgs, f,
		`greatest(g.newest_seen, g.latest_accepted, g.latest_dismissed, g.latest_reopened)`)
	appendScopeKeyFilter(&where, &whereArgs, f, "g.scope_key")
	appendGuardrailStatusFilter(&where, f.Status)

	inner := guardrailViolationAgg + ` ` + guardrailWindowWhere + ` GROUP BY scope_type, scope_key, rule_key, offending`
	args := append(sinceArgs(f.Since), whereArgs...)
	args = append(args, uint64(limit))

	q := fmt.Sprintf(`SELECT g.scope_key, g.rule_key, g.offending, g.occurrences, g.max_score, g.first_seen, g.newest_seen, g.latest_accepted, g.latest_dismissed, g.latest_reopened, g.last_trace_id
		 FROM (%s) AS g
		 %s
		 ORDER BY g.max_score DESC, g.newest_seen DESC
		 LIMIT ?`, inner, where.String())

	return queryRows(ctx, r.conn, "list guardrail violations", q, scanGuardrailViolationGroup, args...)
}

// GetGuardrailViolation returns one group by identity, ok=false when unknown.
func (r *Repo) GetGuardrailViolation(ctx context.Context, scopeKey string, rule repo.RuleKey, offending string) (repo.GuardrailViolationGroup, bool, error) {
	inner := guardrailViolationAgg + `
		WHERE scope_type = ? AND scope_key = ? AND rule_key = ? AND offending = ?
		GROUP BY scope_type, scope_key, rule_key, offending`
	return queryRow(ctx, r.conn, "get guardrail violation",
		`SELECT g.scope_key, g.rule_key, g.offending, g.occurrences, g.max_score, g.first_seen, g.newest_seen, g.latest_accepted, g.latest_dismissed, g.latest_reopened, g.last_trace_id
		 FROM (`+inner+`) AS g
		 LIMIT 1`,
		scanGuardrailViolationGroup,
		string(repo.ScopeService), scopeKey, string(rule), offending)
}

func scanGuardrailViolationGroup(s rowScanner) (repo.GuardrailViolationGroup, error) {
	var (
		scopeKey, ruleKey, offending, lastTraceID                string
		occ                                                      uint64
		maxScore                                                 int32
		firstSeen, lastSeen, acceptedAt, dismissedAt, reopenedAt time.Time
	)
	if err := s.Scan(&scopeKey, &ruleKey, &offending, &occ, &maxScore, &firstSeen, &lastSeen, &acceptedAt, &dismissedAt, &reopenedAt, &lastTraceID); err != nil {
		return repo.GuardrailViolationGroup{}, err
	}
	namespace, service := splitServiceScopeKey(scopeKey)
	return repo.GuardrailViolationGroup{
		Service:     service,
		Namespace:   namespace,
		ScopeKey:    scopeKey,
		RuleKey:     repo.RuleKey(ruleKey),
		Offending:   offending,
		Occurrences: int64(occ),
		MaxScore:    int(maxScore),
		FirstSeen:   firstSeen,
		LastSeen:    lastSeen,
		LastTraceID: lastTraceID,
		Status:      guardrailStatus(acceptedAt, dismissedAt, reopenedAt),
	}, nil
}

// guardrailStatus picks the lifecycle bucket from the latest decision timestamp, accept winning ties.
func guardrailStatus(acceptedAt, dismissedAt, reopenedAt time.Time) repo.GuardrailViolationStatus {
	switch terminal, dismissed := stickyDecisionStatus(acceptedAt, dismissedAt, reopenedAt); {
	case terminal:
		return repo.GuardrailViolationAccepted
	case dismissed:
		return repo.GuardrailViolationDismissed
	default:
		return repo.GuardrailViolationOpen
	}
}

// splitServiceScopeKey splits a "namespace/service" scope key; a key with no
// slash is treated as a bare service in the empty namespace.
func splitServiceScopeKey(key string) (namespace, service string) {
	if i := strings.Index(key, "/"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// GetServiceProfile derives a service's communication summary from the existing
// D1 (call-edge) and D2 (egress) baselines - no dedicated store. It is a
// cold-path UI query (guardrail editor seeding), so scanning the D1 members with
// JSON extraction is fine.
//
//   - Callers: from.svc of every D1 edge whose to.svc is this service. The
//     inbound edge is learned in the CALLER's transaction baseline (its subtree
//     includes this service's entry), so callers come from edges across ALL
//     transactions, not just this service's own rows.
//   - Callees: to.svc of every D1 edge whose from.svc is this service.
//   - Egress: host of every D2 peer learned by this service's transactions.
//
// Matching is by service NAME (D1 edges carry no namespace); egress is scoped by
// (namespace, service) via the transactions table.
func (r *Repo) GetServiceProfile(ctx context.Context, namespace, service string) (repo.ServiceProfile, error) {
	profile := repo.ServiceProfile{Namespace: namespace, Service: service}

	callers, err := r.distinctEdgePeers(ctx, "from", "to", service)
	if err != nil {
		return profile, err
	}
	profile.Callers = callers

	callees, err := r.distinctEdgePeers(ctx, "to", "from", service)
	if err != nil {
		return profile, err
	}
	profile.Callees = callees

	egress, err := r.distinctEgressHosts(ctx, namespace, service)
	if err != nil {
		return profile, err
	}
	profile.Egress = egress

	txs, err := r.ListTransactionStats(ctx, repo.TransactionListParams{
		Namespace: namespace,
		Service:   service,
		Limit:     10000,
	})
	if err != nil {
		return profile, err
	}
	idents := make([]string, 0, len(txs))
	for _, tx := range txs {
		idents = append(idents, repo.TransactionIdentity(tx.Kind, tx.Operation))
	}
	sort.Strings(idents)
	profile.Transactions = idents

	return profile, nil
}

// distinctEdgePeers returns the distinct peer services on one side of the D1
// edges anchored to service on the other side. selectSide/matchSide are "from"
// or "to": to list callers pass select="from", match="to"; to list callees pass
// select="to", match="from". Self-edges (peer == service) are excluded.
func (r *Repo) distinctEdgePeers(ctx context.Context, selectSide, matchSide, service string) ([]string, error) {
	query := fmt.Sprintf(
		`SELECT DISTINCT JSONExtractString(member, '%s', 'svc') AS peer
		 FROM baseline_set_members FINAL
		 WHERE class = ?
		   AND JSONExtractString(member, '%s', 'svc') = ?
		   AND peer != '' AND peer != ?
		 ORDER BY peer ASC`, selectSide, matchSide)
	return queryRows(ctx, r.conn, "service profile edges", query, scanString,
		string(repo.D1CallEdges), service, service)
}

// distinctEgressHosts returns the distinct direct external destinations this
// (namespace, service) reached, from its D2 egress baselines. D2 is
// transaction-scoped and records every client peer in a subtree, so three
// filters narrow it to genuine, service-scoped egress:
//
//   - owner == service - calls this service made, not a downstream dependency's.
//   - external - the per-trace signal that the call didn't reach an instrumented
//     service in-trace.
//   - host is not a known service in this namespace - the cross-trace guard: the
//     external flag misfires when a callee's SERVER span didn't nest under the
//     caller, so a stale external=true row can linger. Scoped to this namespace
//     because a bare hostname resolves in-namespace.
//
// A raw IP matches no service name and still shows up (mapping ClusterIPs back to
// services is future work). Each dest is "host:port", or "host" when no port was
// captured.
func (r *Repo) distinctEgressHosts(ctx context.Context, namespace, service string) ([]string, error) {
	return queryRows(ctx, r.conn, "service profile egress",
		`SELECT DISTINCT
		     concat(
		         JSONExtractString(member, 'host'),
		         if(JSONExtractInt(member, 'port') > 0, concat(':', toString(JSONExtractInt(member, 'port'))), '')
		     ) AS dest
		 FROM baseline_set_members FINAL
		 WHERE class = ?
		   AND transaction_id IN (
		       SELECT transaction_id FROM transactions FINAL
		       WHERE namespace = ? AND service = ?
		   )
		   AND JSONExtractString(member, 'host') != ''
		   AND JSONExtractString(member, 'owner') = ?
		   AND JSONExtractBool(member, 'external')
		   AND arrayElement(splitByChar('.', JSONExtractString(member, 'host')), 1)
		       NOT IN (SELECT DISTINCT service FROM transactions FINAL WHERE namespace = ?)
		 ORDER BY dest ASC`,
		scanString, string(repo.D2Egress), namespace, service, service, namespace)
}
