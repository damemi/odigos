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

// ListServiceGuardrails returns every declared guardrail across all scopes.
func (r *Repo) ListServiceGuardrails(ctx context.Context) ([]repo.ServiceGuardrail, error) {
	return queryRows(ctx, r.pool, "list service guardrails",
		`SELECT scope_type, scope_key, rule_lists, rule_modes, rule_origins, rule_fingerprints, rule_correlations, notify_muted, excluded_integration_ids
		 FROM service_guardrails
		 ORDER BY scope_type ASC, scope_key ASC`,
		scanServiceGuardrail)
}

func scanServiceGuardrail(s rowScanner) (repo.ServiceGuardrail, error) {
	var (
		scopeType        string
		scopeKey         string
		ruleLists        map[string][]string
		ruleModes        map[string]string
		ruleOrigins      map[string]string
		ruleFingerprints map[string]string
		ruleCorrelations map[string]string // rule_key -> JSON([]CorrelationSpec)
		muted            bool
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

// UpsertServiceGuardrail writes one guardrail, keyed by (scope, scopeKey),
// replacing any previous version.
func (r *Repo) UpsertServiceGuardrail(ctx context.Context, guardrail repo.ServiceGuardrail) error {
	ruleLists := make(map[string][]string, len(guardrail.RuleLists))
	for key, list := range guardrail.RuleLists {
		ruleLists[string(key)] = nonNilStrings(list)
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
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO service_guardrails (scope_type, scope_key, rule_lists, rule_modes, rule_origins, rule_fingerprints, rule_correlations, notify_muted, excluded_integration_ids, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (scope_type, scope_key) DO UPDATE SET
			rule_lists = EXCLUDED.rule_lists, rule_modes = EXCLUDED.rule_modes, rule_origins = EXCLUDED.rule_origins,
			rule_fingerprints = EXCLUDED.rule_fingerprints, rule_correlations = EXCLUDED.rule_correlations,
			notify_muted = EXCLUDED.notify_muted, excluded_integration_ids = EXCLUDED.excluded_integration_ids,
			updated_at = EXCLUDED.updated_at`,
		string(scope), guardrail.ScopeKey, ruleLists, ruleModes, ruleOrigins, ruleFingerprints, ruleCorrelations,
		guardrail.Delivery.Muted, nonNilStrings(guardrail.Delivery.ExcludedIntegrationIDs), time.Now()); err != nil {
		return fmt.Errorf("upsert service guardrail: %w", err)
	}
	return nil
}

// DeleteServiceGuardrail drops one scoped guardrail.
func (r *Repo) DeleteServiceGuardrail(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	if _, err := r.pool.Exec(ctx,
		"DELETE FROM service_guardrails WHERE scope_type = $1 AND scope_key = $2",
		string(repo.NormalizePolicyScope(scope)), scopeKey); err != nil {
		return fmt.Errorf("delete service guardrail %s/%s: %w", scope, scopeKey, err)
	}
	return nil
}

// upsertGuardrailViolationSQL merges an occurrence or a decision into its
// violation group: counts add up, score/last_seen/decisions keep the max,
// first_seen keeps the min, and last_trace_id follows the newest occurrence.
const upsertGuardrailViolationSQL = `INSERT INTO guardrail_violations AS g
	(scope_type, scope_key, rule_key, offending, occurrences, max_score, first_seen, last_seen, accepted_at, dismissed_at, reopened_at, last_trace_id)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	ON CONFLICT (scope_type, scope_key, rule_key, offending) DO UPDATE SET
		occurrences   = g.occurrences + EXCLUDED.occurrences,
		max_score     = GREATEST(g.max_score, EXCLUDED.max_score),
		first_seen    = LEAST(g.first_seen, EXCLUDED.first_seen),
		last_seen     = GREATEST(g.last_seen, EXCLUDED.last_seen),
		accepted_at   = GREATEST(g.accepted_at, EXCLUDED.accepted_at),
		dismissed_at  = GREATEST(g.dismissed_at, EXCLUDED.dismissed_at),
		reopened_at   = GREATEST(g.reopened_at, EXCLUDED.reopened_at),
		last_trace_id = CASE WHEN EXCLUDED.last_seen >= g.last_seen THEN EXCLUDED.last_trace_id ELSE g.last_trace_id END`

// RecordGuardrailViolations merges one occurrence group per hit. An occurrence
// never touches the decision timestamps (all epoch), so a sticky
// accept/dismiss survives ongoing traffic.
func (r *Repo) RecordGuardrailViolations(ctx context.Context, hits []repo.GuardrailViolationHit) error {
	if len(hits) == 0 {
		return nil
	}
	type keyed struct {
		key  string
		args []any
	}
	rows := make([]keyed, 0, len(hits))
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
		scope := string(repo.NormalizePolicyScope(h.Scope))
		rows = append(rows, keyed{
			key: scope + "\x00" + h.ScopeKey + "\x00" + string(h.RuleKey) + "\x00" + h.Offending,
			args: []any{
				scope, h.ScopeKey, string(h.RuleKey), h.Offending,
				int64(occ), int32(h.MaxScore), firstSeen, lastSeen, epoch, epoch, epoch, h.LastTraceID,
			},
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	batch := make([][]any, len(rows))
	for i := range rows {
		batch[i] = rows[i].args
	}
	if err := execBatch(ctx, r.pool, upsertGuardrailViolationSQL, batch); err != nil {
		return fmt.Errorf("record guardrail violations: %w", err)
	}
	return nil
}

// DeleteGuardrailViolationsForRule drops every violation group for one rule
// key. Used on transaction inventory reset.
func (r *Repo) DeleteGuardrailViolationsForRule(ctx context.Context, rule repo.RuleKey) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM guardrail_violations WHERE rule_key = $1`, string(rule)); err != nil {
		return fmt.Errorf("delete guardrail violations for rule %s: %w", rule, err)
	}
	return nil
}

// ResolveGuardrailViolation stamps the column for the chosen resolution with
// now. Accept/dismiss are sticky (ongoing occurrences bump the count, not the
// decision); reopen stamps a fresh reopened_at that outdates a prior dismiss,
// moving the group back to the open tab for re-triage.
func (r *Repo) ResolveGuardrailViolation(ctx context.Context, scope repo.PolicyScope, scopeKey string, rule repo.RuleKey, offending string, resolution repo.GuardrailResolution) error {
	acceptedAt, dismissedAt, reopenedAt := epoch, epoch, epoch
	switch resolution {
	case repo.GuardrailResolveAccept:
		acceptedAt = time.Now()
	case repo.GuardrailResolveReopen:
		reopenedAt = time.Now()
	default:
		dismissedAt = time.Now()
	}
	if _, err := r.pool.Exec(ctx, upsertGuardrailViolationSQL,
		string(repo.NormalizePolicyScope(scope)), scopeKey, string(rule), offending,
		int64(0), int32(0), firstSeenFuture, epoch, acceptedAt, dismissedAt, reopenedAt, "",
	); err != nil {
		return fmt.Errorf("resolve guardrail violation %s/%s/%s: %w", scopeKey, rule, offending, err)
	}
	return nil
}

const guardrailViolationCols = `g.scope_key, g.rule_key, g.offending, g.occurrences, g.max_score, g.first_seen, g.last_seen,
	g.accepted_at, g.dismissed_at, g.reopened_at, g.last_trace_id`

// ListGuardrailViolations returns findings over the window with their derived
// status (open/dismissed/accepted), one row per group, worst/most-recent first.
func (r *Repo) ListGuardrailViolations(ctx context.Context, f repo.FindingListFilter) ([]repo.GuardrailViolationGroup, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	var (
		where strings.Builder
		a     args
	)
	appendActivityWindowFilter(&where, &a, f, "g")
	appendScopeKeyFilter(&where, &a, f, "g.scope_key")
	appendGuardrailStatusFilter(&where, f.Status)

	q := fmt.Sprintf(`SELECT %s
		 FROM guardrail_violations AS g
		 %s
		 ORDER BY g.max_score DESC, g.last_seen DESC
		 LIMIT %s`, guardrailViolationCols, where.String(), a.add(limit))

	return queryRows(ctx, r.pool, "list guardrail violations", q, scanGuardrailViolationGroup, a...)
}

// GetGuardrailViolation returns one group by identity, ok=false when unknown.
func (r *Repo) GetGuardrailViolation(ctx context.Context, scopeKey string, rule repo.RuleKey, offending string) (repo.GuardrailViolationGroup, bool, error) {
	return queryRow(ctx, r.pool, "get guardrail violation",
		`SELECT `+guardrailViolationCols+`
		 FROM guardrail_violations AS g
		 WHERE g.scope_type = $1 AND g.scope_key = $2 AND g.rule_key = $3 AND g.offending = $4`,
		scanGuardrailViolationGroup,
		string(repo.ScopeService), scopeKey, string(rule), offending)
}

func scanGuardrailViolationGroup(s rowScanner) (repo.GuardrailViolationGroup, error) {
	var (
		scopeKey, ruleKey, offending, lastTraceID                string
		occ                                                      int64
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
		Occurrences: occ,
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
// cold-path UI query (guardrail editor seeding).
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
// select="to", match="from". Self-edges (peer == service) are excluded. The
// MATERIALIZED CTE keeps the JSON cast to D1 rows only.
func (r *Repo) distinctEdgePeers(ctx context.Context, selectSide, matchSide, service string) ([]string, error) {
	query := fmt.Sprintf(
		`WITH d1 AS MATERIALIZED (
			SELECT member::jsonb AS m FROM baseline_set_members WHERE class = $1
		 )
		 SELECT DISTINCT peer FROM (
			SELECT COALESCE(m #>> '{%s,svc}', '') AS peer, COALESCE(m #>> '{%s,svc}', '') AS anchor FROM d1
		 ) AS e
		 WHERE anchor = $2 AND peer <> '' AND peer <> $3
		 ORDER BY peer ASC`, selectSide, matchSide)
	return queryRows(ctx, r.pool, "service profile edges", query, scanString,
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
// A raw IP matches no service name and still shows up. Each dest is
// "host:port", or "host" when no port was captured.
func (r *Repo) distinctEgressHosts(ctx context.Context, namespace, service string) ([]string, error) {
	return queryRows(ctx, r.pool, "service profile egress",
		`WITH d2 AS MATERIALIZED (
			SELECT member::jsonb AS m
			FROM baseline_set_members
			WHERE class = $1
			  AND transaction_id IN (SELECT transaction_id FROM transactions WHERE namespace = $2 AND service = $3)
		 ), peers AS (
			SELECT COALESCE(m->>'host', '') AS host,
			       COALESCE((m->>'port')::bigint, 0) AS port,
			       COALESCE(m->>'owner', '') AS owner,
			       COALESCE((m->>'external')::boolean, false) AS external
			FROM d2
		 )
		 SELECT DISTINCT host || CASE WHEN port > 0 THEN ':' || port::text ELSE '' END AS dest
		 FROM peers
		 WHERE host <> ''
		   AND owner = $4
		   AND external
		   AND split_part(host, '.', 1) NOT IN (SELECT service FROM transactions WHERE namespace = $5)
		 ORDER BY dest ASC`,
		scanString, string(repo.D2Egress), namespace, service, service, namespace)
}
