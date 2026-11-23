package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// ---------- interrogations ----------
//
// A run is two ADK sessions (adk.sessions, written by the session store in
// internal/interrogation): the run's, app 'interrogation', and its control
// session, app 'interrogation.ctl', both with the run's id as user and session
// id. Every statement of ours that reads ADK's tables, apart from the session
// store's writes and the probe insert's locks, is in this file. Statements
// that compare with the clock take now from the caller.

// interrogationSelect reads a run from its session r, its control session c
// and the counts of its changes.
const interrogationSelect = `SELECT
	r.id, coalesce(r.create_time, 'epoch'), coalesce(r.state->>'open', '') = 'true',
	r.state->'request', coalesce(r.state ? 'run', false),
	coalesce(r.state->'run'->>'status', ''), coalesce(r.state->'run'->>'end_reason', ''),
	coalesce(r.state->'run'->>'error', ''), coalesce((r.state->'run'->>'seq')::bigint, 0),
	r.state->'closed', c.state, r.state->'run',
	ch.stored, ch.live, ch.reverting, ch.reverted, ch.expired, ch.failed, ch.next_expiry_at
FROM adk.sessions r
LEFT JOIN adk.sessions c ON c.app_name = 'interrogation.ctl' AND c.user_id = r.user_id AND c.id = r.id` +
	interrogationCountJoin

// interrogationCountJoin counts the run's changes by their wire state at $1;
// $2 is repo.ChangeFailureReasons. The predicates are those of
// repo.InterrogationChange.State.
const interrogationCountJoin = `
LEFT JOIN LATERAL (
	SELECT count(*) AS stored,
		count(*) FILTER (WHERE p.live) AS live,
		count(*) FILTER (WHERE NOT p.live AND p.in_rule) AS reverting,
		count(*) FILTER (WHERE p.gone AND p.state = 'reverted' AND NOT p.end_reason = ANY($2)) AS reverted,
		count(*) FILTER (WHERE p.gone AND p.state = 'approved') AS expired,
		count(*) FILTER (WHERE p.gone AND p.state = 'reverted' AND p.end_reason = ANY($2)) AS failed,
		min(p.expires_at) FILTER (WHERE p.live) AS next_expiry_at
	FROM (
		SELECT state, expires_at, in_rule, end_reason,
			state = 'approved' AND expires_at > $1 AS live,
			NOT in_rule AND NOT (state = 'approved' AND expires_at > $1) AS gone
		FROM interrogation_requested_probes WHERE interrogation_id = r.id
	) p
) ch ON true`

func scanInterrogation(s rowScanner) (repo.Interrogation, error) {
	var (
		in                       repo.Interrogation
		request, closed, control []byte
		nextExpiry               *time.Time
	)
	c := &in.Changes
	if err := s.Scan(&in.ID, &in.CreatedAt, &in.Open, &request, &in.Started,
		&in.RunStatus, &in.RunEndReason, &in.RunError, &in.Seq, &closed, &control, &in.Run,
		&c.Stored, &c.Live, &c.Reverting, &c.Reverted, &c.Expired, &c.Failed, &nextExpiry); err != nil {
		return repo.Interrogation{}, err
	}
	if nextExpiry != nil {
		c.NextExpiryAt = *nextExpiry
	}
	if len(request) > 0 {
		if err := json.Unmarshal(request, &in.Request); err != nil {
			return repo.Interrogation{}, fmt.Errorf("decode interrogation %s request: %w", in.ID, err)
		}
	}
	if len(closed) > 0 && string(closed) != "null" {
		in.Closed = &repo.InterrogationClosed{}
		if err := json.Unmarshal(closed, in.Closed); err != nil {
			return repo.Interrogation{}, fmt.Errorf("decode interrogation %s closed: %w", in.ID, err)
		}
	}
	if len(control) > 0 {
		if err := json.Unmarshal(control, &in.Control); err != nil {
			return repo.Interrogation{}, fmt.Errorf("decode interrogation %s control: %w", in.ID, err)
		}
	}
	return in, nil
}

// GetInterrogation returns one run with its change counts at now; ok=false
// when unknown.
func (r *Repo) GetInterrogation(ctx context.Context, id string, now time.Time) (repo.Interrogation, bool, error) {
	return queryRow(ctx, r.pool, "get interrogation",
		interrogationSelect+`
WHERE r.app_name = 'interrogation' AND r.user_id = $3 AND r.id = $3`,
		scanInterrogation, now, repo.ChangeFailureReasons, id)
}

// ListInterrogations returns runs newest first. before is the id of the last
// run of the previous page; an id that does not exist gives an empty page.
func (r *Repo) ListInterrogations(ctx context.Context, before string, limit int, now time.Time) ([]repo.Interrogation, error) {
	q, a := listInterrogationsQuery(before, limit, now)
	return queryRows(ctx, r.pool, "list interrogations", q, scanInterrogation, a...)
}

func listInterrogationsQuery(before string, limit int, now time.Time) (string, []any) {
	a := args{now, repo.ChangeFailureReasons}
	q := interrogationSelect + `
WHERE r.app_name = 'interrogation'`
	if before != "" {
		// The row comparison matches the order and interrogation_created_idx,
		// so runs that share a create_time are neither skipped nor repeated.
		ph := a.add(before)
		q += ` AND (r.create_time, r.id) < (
	SELECT create_time, id FROM adk.sessions WHERE app_name = 'interrogation' AND user_id = ` + ph + ` AND id = ` + ph + `)`
	}
	q += `
ORDER BY r.create_time DESC, r.id DESC LIMIT ` + a.add(limit)
	return q, a
}

// InterrogationSummary counts the runs open now and sums up every run the
// server keeps.
func (r *Repo) InterrogationSummary(ctx context.Context) (repo.InterrogationSummary, error) {
	var (
		s       repo.InterrogationSummary
		sources []byte
	)
	err := r.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE open AND NOT started),
	count(*) FILTER (WHERE open AND started),
	coalesce(jsonb_agg(source ORDER BY create_time) FILTER (WHERE open AND started), '[]'),
	count(*),
	count(*) FILTER (WHERE NOT open),
	count(*) FILTER (WHERE NOT open AND started_at > 'epoch'),
	coalesce(avg(round) FILTER (WHERE NOT open AND started_at > 'epoch'), 0),
	coalesce(avg(extract(epoch FROM ended_at - started_at)) FILTER (WHERE NOT open AND started_at > 'epoch'), 0),
	coalesce(sum(model_calls), 0)::bigint, coalesce(sum(input_tokens), 0)::bigint, coalesce(sum(output_tokens), 0)::bigint,
	coalesce((
		SELECT state->'run'->>'model' FROM adk.sessions
		WHERE app_name = 'interrogation' AND coalesce(state->'run'->>'model', '') <> ''
		ORDER BY update_time DESC LIMIT 1), '')
FROM (
	SELECT create_time,
		coalesce(state->>'open', '') = 'true' AS open,
		state ? 'run' AS started,
		state->'request'->'source' AS source,
		(state->'run'->>'started_at')::timestamptz AS started_at,
		coalesce((state->'closed'->>'at')::timestamptz, (state->'run'->>'ended_at')::timestamptz) AS ended_at,
		coalesce((state->'run'->>'round')::int, 0) AS round,
		coalesce((state->'run'->>'model_calls')::bigint, 0) AS model_calls,
		coalesce((state->'run'->>'input_tokens')::bigint, 0) AS input_tokens,
		coalesce((state->'run'->>'output_tokens')::bigint, 0) AS output_tokens
	FROM adk.sessions WHERE app_name = 'interrogation'
) s`).Scan(&s.Queued, &s.Running, &sources, &s.Total, &s.Ended, &s.Averaged, &s.AvgRounds, &s.AvgDurationSeconds,
		&s.ModelCalls, &s.InputTokens, &s.OutputTokens, &s.Model)
	if err != nil {
		return repo.InterrogationSummary{}, fmt.Errorf("interrogation summary: %w", err)
	}
	if err := json.Unmarshal(sources, &s.RunningSources); err != nil {
		return repo.InterrogationSummary{}, fmt.Errorf("decode running sources: %w", err)
	}
	return s, nil
}

// InterrogationWorkloadHolder returns the open run of a workload
// (namespace/Kind/name); ok=false when it has none.
func (r *Repo) InterrogationWorkloadHolder(ctx context.Context, workload string) (repo.InterrogationHolder, bool, error) {
	return queryRow(ctx, r.pool, "interrogation workload holder", `
SELECT id, create_time, state ? 'run' FROM adk.sessions
WHERE app_name = 'interrogation' AND state->>'open' = 'true' AND state->>'workload' = $1`,
		func(s rowScanner) (repo.InterrogationHolder, error) {
			var h repo.InterrogationHolder
			err := s.Scan(&h.ID, &h.CreatedAt, &h.Started)
			return h, err
		}, workload)
}

const openInterrogationsQuery = `
SELECT r.id, r.state, r.update_time, c.state
FROM adk.sessions r
LEFT JOIN adk.sessions c ON c.app_name = 'interrogation.ctl' AND c.user_id = r.user_id AND c.id = r.id
WHERE r.app_name = 'interrogation' AND r.state->>'open' = 'true'
ORDER BY r.create_time, r.id`

// ListOpenInterrogations returns every open run with its control session,
// oldest first, for the leader's poll.
func (r *Repo) ListOpenInterrogations(ctx context.Context) ([]repo.OpenInterrogation, error) {
	return queryRows(ctx, r.pool, "list open interrogations", openInterrogationsQuery,
		func(s rowScanner) (repo.OpenInterrogation, error) {
			var (
				o              repo.OpenInterrogation
				state, control []byte
			)
			err := s.Scan(&o.ID, &state, &o.UpdateTime, &control)
			o.State, o.Control = state, control
			return o, err
		})
}

// ListInterrogationEvents returns up to limit items of a run's event log with
// a sequence after after; ok=false when the run is unknown. One ADK event
// carries a list of items, so the page can end inside one: the next page
// reads that event again and drops the items it already returned.
func (r *Repo) ListInterrogationEvents(ctx context.Context, id string, after int64, limit int) (repo.InterrogationEvents, bool, error) {
	// The run is read before its events: an event read after the run closed
	// is the run's last, so complete never counts one that is still to come.
	type runSeq struct {
		open bool
		seq  int64
	}
	run, ok, err := queryRow(ctx, r.pool, "get interrogation sequence", `
SELECT coalesce(state->>'open', '') = 'true', coalesce((state->'run'->>'seq')::bigint, 0)
FROM adk.sessions WHERE app_name = 'interrogation' AND user_id = $1 AND id = $1`,
		func(s rowScanner) (runSeq, error) {
			var v runSeq
			err := s.Scan(&v.open, &v.seq)
			return v, err
		}, id)
	if err != nil || !ok {
		return repo.InterrogationEvents{}, ok, err
	}

	lists, err := queryRows(ctx, r.pool, "list interrogation events", `
SELECT custom_metadata->'interrogation'
FROM adk.events
WHERE app_name = 'interrogation' AND user_id = $1 AND session_id = $1
	AND (custom_metadata->>'interrogation_seq')::bigint > $2
ORDER BY (custom_metadata->>'interrogation_seq')::bigint
LIMIT $3`, func(s rowScanner) ([]byte, error) {
		var b []byte
		err := s.Scan(&b)
		return b, err
	}, id, after, limit)
	if err != nil {
		return repo.InterrogationEvents{}, false, err
	}

	page := repo.InterrogationEvents{Items: []repo.InterrogationEvent{}, LastSequence: after}
	for _, list := range lists {
		var items []json.RawMessage
		if err := json.Unmarshal(list, &items); err != nil {
			return repo.InterrogationEvents{}, false, fmt.Errorf("decode interrogation %s events: %w", id, err)
		}
		for _, item := range items {
			var head struct {
				Seq int64 `json:"seq"`
			}
			if err := json.Unmarshal(item, &head); err != nil {
				return repo.InterrogationEvents{}, false, fmt.Errorf("decode interrogation %s event: %w", id, err)
			}
			if head.Seq <= page.LastSequence || len(page.Items) == limit {
				continue
			}
			page.Items = append(page.Items, repo.InterrogationEvent{Seq: head.Seq, JSON: item})
			page.LastSequence = head.Seq
		}
	}
	page.Complete = !run.open && len(page.Items) < limit && page.LastSequence >= run.seq
	return page, true, nil
}

// ListInterrogationSources returns the workloads with call-graph evidence,
// with the open run of each and its live changes at now.
func (r *Repo) ListInterrogationSources(ctx context.Context, limit int, now time.Time) ([]repo.InterrogationSourceStat, error) {
	return queryRows(ctx, r.pool, "list interrogation sources", `
WITH t AS (
	SELECT DISTINCT k.namespace, k.workload_kind, k.workload_name, n.transaction_id
	FROM autopilot_call_tree_nodes n
	JOIN k8s_source_identity k USING (source_id)
)
SELECT t.namespace, t.workload_kind, t.workload_name,
	count(DISTINCT t.transaction_id), coalesce(max(a.last_seen), 'epoch'), coalesce(o.id, ''),
	(SELECT count(*) FROM interrogation_requested_probes p
		WHERE p.namespace = t.namespace AND p.workload_kind = t.workload_kind AND p.workload_name = t.workload_name
			AND p.state = 'approved' AND p.expires_at > $2)
FROM t
LEFT JOIN transaction_activity a ON a.transaction_id = t.transaction_id
LEFT JOIN adk.sessions o ON o.app_name = 'interrogation' AND o.state->>'open' = 'true'
	AND o.state->>'workload' = t.namespace || '/' || t.workload_kind || '/' || t.workload_name
GROUP BY t.namespace, t.workload_kind, t.workload_name, o.id
ORDER BY t.namespace, t.workload_name, t.workload_kind
LIMIT $1`,
		func(s rowScanner) (repo.InterrogationSourceStat, error) {
			var v repo.InterrogationSourceStat
			err := s.Scan(&v.Source.Namespace, &v.Source.Kind, &v.Source.Name,
				&v.Transactions, &v.LastSeen, &v.OpenID, &v.LiveChanges)
			v.LastSeen = normalizeEpoch(v.LastSeen)
			return v, err
		}, limit, now)
}

// ListInterrogationSourceTransactions returns a workload's transactions that
// have a call graph, busiest first.
func (r *Repo) ListInterrogationSourceTransactions(ctx context.Context, source repo.InterrogationSource, limit int) ([]repo.InterrogationSourceTransaction, error) {
	return queryRows(ctx, r.pool, "list interrogation source transactions", `
SELECT tr.transaction_id, tr.operation, tr.entry_kind, coalesce(a.volume, 0), coalesce(a.last_seen, 'epoch'), f.functions
FROM (
	SELECT n.transaction_id, count(DISTINCT n.function_name) AS functions
	FROM autopilot_call_tree_nodes n
	JOIN k8s_source_identity k USING (source_id)
	WHERE k.namespace = $1 AND k.workload_kind = $2 AND k.workload_name = $3
	GROUP BY 1
) f
JOIN transactions tr ON tr.transaction_id = f.transaction_id
LEFT JOIN transaction_activity a ON a.transaction_id = tr.transaction_id
ORDER BY a.volume DESC NULLS LAST, tr.operation, tr.transaction_id
LIMIT $4`,
		func(s rowScanner) (repo.InterrogationSourceTransaction, error) {
			var v repo.InterrogationSourceTransaction
			err := s.Scan(&v.TransactionID, &v.Operation, &v.Kind, &v.Volume, &v.LastSeen, &v.Functions)
			v.LastSeen = normalizeEpoch(v.LastSeen)
			return v, err
		}, source.Namespace, source.Kind, source.Name, limit)
}

// ---------- interrogation changes ----------

const changeSelect = `SELECT
	change_id, round, first_seen, language,
	class_name, method_name, package_name, function_name, receiver_name, receiver_method_name, signature,
	state, expires_at, end_reason, in_rule, applied_at, removed_at, block_reason, updated_at
FROM interrogation_requested_probes`

func scanChange(s rowScanner) (repo.InterrogationChange, error) {
	var c repo.InterrogationChange
	if err := s.Scan(&c.ChangeID, &c.Round, &c.FirstSeen, &c.Language,
		&c.ClassName, &c.MethodName, &c.PackageName, &c.FunctionName, &c.ReceiverName, &c.ReceiverMethodName, &c.Signature,
		&c.Desired, &c.ExpiresAt, &c.EndReason, &c.InRule, &c.AppliedAt, &c.RemovedAt, &c.BlockReason, &c.UpdatedAt,
	); err != nil {
		return repo.InterrogationChange{}, err
	}
	for _, t := range []*time.Time{&c.FirstSeen, &c.ExpiresAt, &c.AppliedAt, &c.RemovedAt, &c.UpdatedAt} {
		*t = normalizeEpoch(*t)
	}
	return c, nil
}

// GetInterrogationChange returns one change; ok=false when unknown.
func (r *Repo) GetInterrogationChange(ctx context.Context, changeID string) (repo.InterrogationChange, bool, error) {
	return queryRow(ctx, r.pool, "get interrogation change", changeSelect+`
WHERE change_id = $1`, scanChange, changeID)
}

// ListInterrogationChanges returns every stored change of one run, oldest
// first.
func (r *Repo) ListInterrogationChanges(ctx context.Context, interrogationID string) ([]repo.InterrogationChange, error) {
	return queryRows(ctx, r.pool, "list interrogation changes", changeSelect+`
WHERE interrogation_id = $1
ORDER BY first_seen, change_id`, scanChange, interrogationID)
}

// InterrogationChangesSummary counts the changes live at now, in all and per
// workload and language.
func (r *Repo) InterrogationChangesSummary(ctx context.Context, now time.Time) (repo.InterrogationChangesSummary, error) {
	type liveChange struct {
		id       string
		source   repo.InterrogationSource
		language string
		expires  time.Time
	}
	rows, err := queryRows(ctx, r.pool, "interrogation changes summary", `
SELECT change_id, namespace, workload_kind, workload_name, language, expires_at
FROM interrogation_requested_probes
WHERE state = 'approved' AND expires_at > $1
ORDER BY namespace, workload_name, workload_kind, language, expires_at, change_id`,
		func(s rowScanner) (liveChange, error) {
			var v liveChange
			err := s.Scan(&v.id, &v.source.Namespace, &v.source.Kind, &v.source.Name, &v.language, &v.expires)
			return v, err
		}, now)
	if err != nil {
		return repo.InterrogationChangesSummary{}, err
	}
	sum := repo.InterrogationChangesSummary{BySource: []repo.InterrogationSourceChanges{}}
	for _, c := range rows {
		sum.Live++
		if sum.NextExpiryAt.IsZero() || c.expires.Before(sum.NextExpiryAt) ||
			c.expires.Equal(sum.NextExpiryAt) && c.id < sum.NextExpiryChangeID {
			sum.NextExpiryAt, sum.NextExpiryChangeID = c.expires, c.id
		}
		if n := len(sum.BySource); n == 0 || sum.BySource[n-1].Source != c.source || sum.BySource[n-1].Language != c.language {
			sum.BySource = append(sum.BySource, repo.InterrogationSourceChanges{Source: c.source, Language: c.language})
		}
		sum.BySource[len(sum.BySource)-1].Live++
	}
	return sum, nil
}

// EndInterrogationChange ends an approved change that never took effect;
// reason becomes its end reason.
func (r *Repo) EndInterrogationChange(ctx context.Context, changeID, reason string, now time.Time) error {
	if _, err := r.pool.Exec(ctx, `
UPDATE interrogation_requested_probes
SET state = 'reverted', end_reason = $2, updated_at = $3
WHERE change_id = $1 AND state = 'approved'`, changeID, reason, now); err != nil {
		return fmt.Errorf("end interrogation change: %w", err)
	}
	return nil
}

// RevertInterrogationProbes is a stop with revert: it reverts the run's
// changes live at now.
func (r *Repo) RevertInterrogationProbes(ctx context.Context, interrogationID string, now time.Time) error {
	if _, err := r.pool.Exec(ctx, `
UPDATE interrogation_requested_probes
SET state = 'reverted', end_reason = 'stop', updated_at = $2
WHERE interrogation_id = $1 AND state = 'approved' AND expires_at > $2`, interrogationID, now); err != nil {
		return fmt.Errorf("revert interrogation probes: %w", err)
	}
	return nil
}

// RevertStoppedInterrogationProbes reverts the changes live at now of every
// run that is closed and whose stop asked for a revert. The leader
// runs it on every poll: it remembers nothing, so it also reverts what a
// process that stopped after the run's closing event meant to, and a repeat
// changes nothing.
func (r *Repo) RevertStoppedInterrogationProbes(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
UPDATE interrogation_requested_probes p
SET state = 'reverted', end_reason = 'stop', updated_at = $1
FROM adk.sessions r
JOIN adk.sessions c ON c.app_name = 'interrogation.ctl' AND c.user_id = r.user_id AND c.id = r.id
WHERE r.app_name = 'interrogation' AND r.user_id = p.interrogation_id AND r.id = p.interrogation_id
	AND r.state->>'open' = 'false' AND c.state->'stop'->>'changes' = 'revert'
	AND p.state = 'approved' AND p.expires_at > $1`, now)
	if err != nil {
		return 0, fmt.Errorf("revert stopped interrogation probes: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------- retention ----------

// pruneInterrogations deletes the runs that ended before cutoff, or before
// unstartedCutoff when no leader picked them up, with their events (ADK's
// cascade) and control sessions, and the changes that ended before cutoff and
// left the rule. Open runs are never deleted, so this does not race the
// workflow; nothing appends to a closed run, so its update_time is the time it
// ended.
func (r *Repo) pruneInterrogations(ctx context.Context, cutoff, unstartedCutoff time.Time, deleted map[string]int64) error {
	n, err := r.deleteInChunks(ctx, "adk.sessions",
		`app_name = 'interrogation' AND state->>'open' = 'false' AND (update_time < $1 OR NOT state ? 'run' AND update_time < $2)`,
		cutoff, unstartedCutoff)
	if err != nil {
		return fmt.Errorf("prune interrogation sessions: %w", err)
	}
	deleted["interrogation_sessions"] = n
	// A control session is created with its run, in one transaction, so one
	// without its run is one whose run was deleted above.
	n, err = r.deleteInChunks(ctx, "adk.sessions",
		`app_name = 'interrogation.ctl' AND create_time < $1 AND NOT EXISTS (SELECT 1 FROM adk.sessions r
			WHERE r.app_name = 'interrogation' AND r.user_id = adk.sessions.user_id AND r.id = adk.sessions.id)`, unstartedCutoff)
	if err != nil {
		return fmt.Errorf("prune interrogation control sessions: %w", err)
	}
	deleted["interrogation_control_sessions"] = n
	n, err = r.deleteInChunks(ctx, "interrogation_requested_probes",
		`NOT in_rule AND NOT being_placed AND greatest(updated_at, expires_at) < $1`, cutoff)
	if err != nil {
		return fmt.Errorf("prune interrogation changes: %w", err)
	}
	deleted["interrogation_changes"] = n
	return nil
}
