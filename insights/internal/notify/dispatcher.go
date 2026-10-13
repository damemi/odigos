package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// PollInterval is how often the dispatcher diffs findings against the ledger.
// Hard-coded for phase 1; it matches the engine's default flush cadence so a
// finding is delivered within roughly one flush of being recorded.
const PollInterval = 15 * time.Second

// DefaultOverlap is how far behind the cursor each tick re-reads. Findings are
// flushed in batches stamped with their trace times, so a row can land with a
// last_seen older than the previous cursor; re-reading two flush intervals
// covers that and the ledger makes the overlap idempotent.
const DefaultOverlap = 2 * PollInterval

// listLimit bounds how many findings one tick considers per kind. Far above
// what a flush interval produces; the ledger catches up on the next tick if hit.
const listLimit = 5000

// dueLimit bounds how many parked retries one tick re-attempts per integration.
const dueLimit = 500

// Dispatcher is the poll loop: for every enabled integration, find what
// changed since its cursor, decide firing/resolved per finding from the
// ledger and the backend's capabilities, deliver, and record outcomes.
//
// It must run on exactly one replica at a time (see leader package). It holds
// no state across ticks beyond what is in ClickHouse, so a replacement leader
// resumes from the cursors.
type Dispatcher struct {
	repo     repo.Repository
	registry *Registry
	log      *slog.Logger
	identity string
	poll     time.Duration
	overlap  time.Duration
	now      func() time.Time
}

// NewDispatcher wires a dispatcher. identity is the replica name recorded on
// cursors (POD_NAME) so the debug UI shows who is sending.
func NewDispatcher(r repo.Repository, reg *Registry, log *slog.Logger, identity string, poll, overlap time.Duration) *Dispatcher {
	if poll <= 0 {
		poll = PollInterval
	}
	if overlap <= 0 {
		overlap = 2 * poll
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{repo: r, registry: reg, log: log.With("component", "notifier"), identity: identity, poll: poll, overlap: overlap, now: time.Now}
}

// Run ticks until ctx is cancelled. One tick runs at a time; a slow backend
// delays the next tick rather than overlapping it.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.poll)
	defer ticker.Stop()
	for {
		if err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("notifier tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce performs a single tick across all enabled integrations. Exported for
// tests and for the debug UI's "run now".
func (d *Dispatcher) RunOnce(ctx context.Context) error {
	start := d.now()
	integrations, err := d.repo.ListIntegrations(ctx)
	if err != nil {
		return fmt.Errorf("list integrations: %w", err)
	}
	var enabled []repo.Integration
	for _, in := range integrations {
		if in.Enabled {
			enabled = append(enabled, in)
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	policies, err := d.repo.ListEnabledPolicies(ctx)
	if err != nil {
		return fmt.Errorf("list policies: %w", err)
	}
	guardrails, err := d.repo.ListServiceGuardrails(ctx)
	if err != nil {
		return fmt.Errorf("list guardrails: %w", err)
	}
	router := NewRouter(policies, guardrails)

	var firstErr error
	for _, in := range enabled {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := d.runIntegration(ctx, in, router, start); err != nil {
			d.log.Error("integration tick failed", "integration", in.ID, "type", in.Type, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	metrics.ObserveNotifierTick(d.now().Sub(start).Seconds())
	return firstErr
}

// candidate is a finding under consideration this tick with its current state.
type candidate struct {
	event Event
	row   *repo.NotificationDispatch
}

func (d *Dispatcher) runIntegration(ctx context.Context, in repo.Integration, router *Router, now time.Time) error {
	sink, desc, err := d.registry.Build(in)
	if err != nil {
		// A misconfigured integration is surfaced through its health, not
		// retried per finding: one audit row per tick with the config error.
		return d.repo.RecordNotificationAttempts(ctx, []repo.NotificationAttempt{{
			IntegrationID: in.ID, Action: repo.DispatchFiring, Attempt: 0,
			Outcome: repo.AttemptError, Error: "config: " + err.Error(), AttemptedAt: now,
		}})
	}

	since := now.Add(-d.overlap)
	if cur, ok, err := d.repo.GetNotificationCursor(ctx, in.ID); err != nil {
		return fmt.Errorf("get cursor: %w", err)
	} else if ok && !cur.Cursor.IsZero() {
		since = cur.Cursor.Add(-d.overlap)
	}

	candidates, err := d.collectCandidates(ctx, in, router, since)
	if err != nil {
		return err
	}
	if err := d.collectDue(ctx, in, router, now, candidates); err != nil {
		return err
	}
	if err := d.attachLedger(ctx, in.ID, candidates); err != nil {
		return err
	}

	events := d.plan(in, desc.Capabilities, candidates, now)
	if len(events) > 0 {
		if err := d.deliver(ctx, in, desc, sink, events, candidates, now); err != nil {
			return err
		}
	}
	return d.repo.SetNotificationCursor(ctx, repo.NotificationCursor{
		IntegrationID: in.ID, Cursor: now, LastRunAt: now, Leader: d.identity,
	})
}

// collectCandidates loads findings active since the window start that route
// to this integration and pass its filters.
func (d *Dispatcher) collectCandidates(ctx context.Context, in repo.Integration, router *Router, since time.Time) (map[string]*candidate, error) {
	out := make(map[string]*candidate)
	filter := repo.FindingListFilter{Since: since, Limit: listLimit}
	anomalies, err := d.repo.ListAnomalyIssues(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list anomalies: %w", err)
	}
	for _, a := range anomalies {
		if !Routes(router.ForAnomaly(a), in.ID) {
			continue
		}
		ev := EventFromAnomaly(a, repo.DispatchFiring)
		if PassesFilters(in.Filters, ev) {
			out[ev.Key] = &candidate{event: ev}
		}
	}
	violations, err := d.repo.ListGuardrailViolations(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list violations: %w", err)
	}
	for _, g := range violations {
		if !Routes(router.ForViolation(g), in.ID) {
			continue
		}
		ev := EventFromViolation(g, repo.DispatchFiring)
		if PassesFilters(in.Filters, ev) {
			out[ev.Key] = &candidate{event: ev}
		}
	}
	return out, nil
}

// collectDue adds parked retries (failed and due, or re-armed pending) whose
// finding fell out of the poll window, rebuilding the event from the current
// finding so a retry carries fresh counts rather than the failed payload.
func (d *Dispatcher) collectDue(ctx context.Context, in repo.Integration, router *Router, now time.Time, candidates map[string]*candidate) error {
	var due []repo.NotificationDispatch
	for _, f := range []repo.DispatchListFilter{
		{IntegrationID: in.ID, State: repo.DispatchFailed, DueBefore: now, Limit: dueLimit},
		{IntegrationID: in.ID, State: repo.DispatchPending, Limit: dueLimit},
	} {
		rows, err := d.repo.ListNotificationDispatch(ctx, f)
		if err != nil {
			return fmt.Errorf("list due dispatch: %w", err)
		}
		due = append(due, rows...)
	}
	var gone []repo.NotificationDispatch
	for i := range due {
		row := due[i]
		if _, ok := candidates[row.FindingKey]; ok {
			continue
		}
		ev, found, err := d.loadFinding(ctx, row.FindingKey)
		if err != nil {
			return err
		}
		if !found {
			row.State = repo.DispatchDead
			row.LastError = "finding no longer exists"
			gone = append(gone, row)
			continue
		}
		// A retry for a finding that no longer routes here is dropped quietly.
		if !d.routes(router, ev, in.ID) || !PassesFilters(in.Filters, ev) {
			continue
		}
		r := row
		candidates[ev.Key] = &candidate{event: ev, row: &r}
	}
	if len(gone) > 0 {
		return d.repo.UpsertNotificationDispatch(ctx, gone)
	}
	return nil
}

func (d *Dispatcher) routes(router *Router, ev Event, integrationID string) bool {
	switch ev.Kind {
	case KindAnomaly:
		return Routes(router.ForAnomaly(repo.AnomalyIssue{PolicyID: ev.PolicyID}), integrationID)
	case KindViolation:
		return Routes(router.ForViolation(repo.GuardrailViolationGroup{ScopeKey: ev.ScopeKey}), integrationID)
	}
	return false
}

// loadFinding rebuilds the event for a ledger key from the current finding.
func (d *Dispatcher) loadFinding(ctx context.Context, key string) (Event, bool, error) {
	kind, rest, ok := strings.Cut(key, ":")
	if !ok {
		return Event{}, false, nil
	}
	switch Kind(kind) {
	case KindAnomaly:
		txn, sig, ok := strings.Cut(rest, ":")
		if !ok {
			return Event{}, false, nil
		}
		id, err := strconv.ParseInt(txn, 10, 64)
		if err != nil {
			return Event{}, false, nil
		}
		a, found, err := d.repo.GetAnomalyIssue(ctx, id, sig)
		if err != nil {
			return Event{}, false, fmt.Errorf("get anomaly %s: %w", key, err)
		}
		if !found {
			return Event{}, false, nil
		}
		return EventFromAnomaly(a, repo.DispatchFiring), true, nil
	case KindViolation:
		// scope_key may itself contain "/" but never ":"; rule and offending
		// are the last two segments and offending may contain ":" so split
		// twice from the left.
		scope, rest2, ok := strings.Cut(rest, ":")
		if !ok {
			return Event{}, false, nil
		}
		rule, offending, ok := strings.Cut(rest2, ":")
		if !ok {
			return Event{}, false, nil
		}
		g, found, err := d.repo.GetGuardrailViolation(ctx, scope, repo.RuleKey(rule), offending)
		if err != nil {
			return Event{}, false, fmt.Errorf("get violation %s: %w", key, err)
		}
		if !found {
			return Event{}, false, nil
		}
		return EventFromViolation(g, repo.DispatchFiring), true, nil
	}
	return Event{}, false, nil
}

// attachLedger loads the ledger rows for the candidates that do not have one yet.
func (d *Dispatcher) attachLedger(ctx context.Context, integrationID string, candidates map[string]*candidate) error {
	keys := make([]string, 0, len(candidates))
	for k, c := range candidates {
		if c.row == nil {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	rows, err := d.repo.ListNotificationDispatch(ctx, repo.DispatchListFilter{IntegrationID: integrationID, FindingKeys: keys, Limit: len(keys)})
	if err != nil {
		return fmt.Errorf("list ledger: %w", err)
	}
	for i := range rows {
		if c, ok := candidates[rows[i].FindingKey]; ok {
			c.row = &rows[i]
		}
	}
	return nil
}

// plan derives the events to send this tick from each candidate's current
// status, its ledger row, and the backend's capabilities.
func (d *Dispatcher) plan(in repo.Integration, caps Capabilities, candidates map[string]*candidate, now time.Time) []Event {
	var out []Event
	for _, c := range candidates {
		ev := c.event
		row := c.row
		due := row != nil && (row.State == repo.DispatchPending ||
			(row.State == repo.DispatchFailed && !row.NextAttemptAt.After(now)))
		open := IsOpen(ev.Status)

		switch {
		case row != nil && row.State == repo.DispatchDead:
			continue
		case open:
			switch {
			case row == nil, row.LastAction == repo.DispatchResolved:
				// New finding, or one re-opened after a resolve was sent.
			case due:
				// Retry the parked firing.
			case row.State == repo.DispatchSent && caps.NeedsHeartbeat:
				// Keep the backend's alert alive while the finding is active.
			default:
				continue
			}
			ev.Action = repo.DispatchFiring
		default:
			wantResolve := caps.SupportsResolve || in.Filters.NotifyOnResolve
			switch {
			case row == nil:
				// Closed before we ever told anyone: nothing to resolve.
				continue
			case row.LastAction == repo.DispatchFiring && row.State == repo.DispatchSent && wantResolve:
			case row.LastAction == repo.DispatchResolved && due:
			default:
				continue
			}
			ev.Action = repo.DispatchResolved
		}
		c.event = ev
		out = append(out, ev)
	}
	return out
}

// deliver sends events in capability-sized batches and records ledger rows
// and audit attempts per finding.
func (d *Dispatcher) deliver(ctx context.Context, in repo.Integration, desc Descriptor, sink Sink, events []Event, candidates map[string]*candidate, now time.Time) error {
	batch := desc.Capabilities.MaxBatch
	if batch < 1 {
		batch = 1
	}
	var (
		ledger   []repo.NotificationDispatch
		attempts []repo.NotificationAttempt
	)
	for start := 0; start < len(events); start += batch {
		end := start + batch
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		hints := ExternalIDHint{}
		for _, ev := range chunk {
			if c := candidates[ev.Key]; c.row != nil && c.row.ExternalID != "" {
				hints[ev.Key] = c.row.ExternalID
			}
		}
		t0 := d.now()
		results := sink.Deliver(ctx, chunk, hints)
		elapsed := d.now().Sub(t0)
		if len(results) != len(chunk) {
			results = AllResults(chunk, 0, Permanent("sink returned %d results for %d events", len(results), len(chunk)))
		}
		for i, res := range results {
			ev := chunk[i]
			row, attempt := d.apply(in.ID, ev, candidates[ev.Key].row, res, now)
			attempt.Duration = elapsed
			attempt.EventCount = len(chunk)
			ledger = append(ledger, row)
			attempts = append(attempts, attempt)
			metrics.IncNotifierDelivery(in.Type, string(attempt.Outcome))
		}
	}
	if err := d.repo.UpsertNotificationDispatch(ctx, ledger); err != nil {
		return err
	}
	return d.repo.RecordNotificationAttempts(ctx, attempts)
}

// apply folds one delivery result into the finding's ledger row and audit entry.
func (d *Dispatcher) apply(integrationID string, ev Event, prev *repo.NotificationDispatch, res DeliveryResult, now time.Time) (repo.NotificationDispatch, repo.NotificationAttempt) {
	row := repo.NotificationDispatch{IntegrationID: integrationID, FindingKey: ev.Key}
	if prev != nil {
		row = *prev
	}
	// A change of action starts a fresh attempt count.
	if row.LastAction != ev.Action {
		row.Attempts = 0
	}
	row.LastAction = ev.Action
	row.OccurrencesAtSend = ev.Occurrences
	row.SeverityAtSend = string(ev.Severity)
	row.StatusAtSend = ev.Status
	if res.ExternalID != "" {
		row.ExternalID = res.ExternalID
	}

	attempt := repo.NotificationAttempt{
		IntegrationID: integrationID, FindingKey: ev.Key, Action: ev.Action,
		Attempt: row.Attempts + 1, HTTPStatus: res.HTTPStatus, AttemptedAt: now,
	}
	switch {
	case res.Err == nil:
		row.State = repo.DispatchSent
		row.Attempts = 0
		row.NextAttemptAt = time.Time{}
		row.LastError = ""
		row.SentAt = now
		attempt.Outcome = repo.AttemptOK
	case !res.Err.Retryable:
		row.State = repo.DispatchDead
		row.Attempts++
		row.NextAttemptAt = time.Time{}
		row.LastError = res.Err.Error()
		attempt.Outcome = repo.AttemptDead
		attempt.Error = row.LastError
	default:
		row.Attempts++
		row.LastError = res.Err.Error()
		attempt.Error = row.LastError
		if row.Attempts >= MaxAttempts {
			row.State = repo.DispatchDead
			row.NextAttemptAt = time.Time{}
			attempt.Outcome = repo.AttemptDead
		} else {
			row.State = repo.DispatchFailed
			row.NextAttemptAt = now.Add(Backoff(row.Attempts))
			attempt.Outcome = repo.AttemptError
		}
	}
	if attempt.HTTPStatus == 0 && res.Err != nil {
		attempt.HTTPStatus = res.Err.HTTPStatus
	}
	return row, attempt
}

// TestIntegration builds the sink for an integration (validating its config)
// and runs its connectivity check. Shared by the API and the debug UI.
func (d *Dispatcher) TestIntegration(ctx context.Context, in repo.Integration) error {
	sink, _, err := d.registry.Build(in)
	if err != nil {
		return err
	}
	return sink.Test(ctx)
}
