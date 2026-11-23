package apimap

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	// changeKind is the one kind of change an interrogation makes today.
	changeKind = "custom_instrumentation"
	// sourceLanguage is the language of every source with evidence: call
	// graphs exist for Java only.
	sourceLanguage = "java"
	gateway        = "litellm"
)

// InterrogationTitle is the run's question, or the default question when none
// was asked.
func InterrogationTitle(req repo.InterrogationRequest) string {
	if req.Question != "" {
		return req.Question
	}
	return "What is worth instrumenting in " + req.Source.Name + "?"
}

// FromInterrogation maps a run, read from its two sessions, to the wire type
// at now. The list leaves out the rounds, the answer and the changes; detail
// adds them, with changes the run's stored changes.
func FromInterrogation(in repo.Interrogation, changes []repo.InterrogationChange, detail bool, now time.Time) (apitypes.Interrogation, error) {
	var st interrogation.State
	if len(in.Run) > 0 {
		if err := json.Unmarshal(in.Run, &st); err != nil {
			return apitypes.Interrogation{}, fmt.Errorf("decode interrogation %s: %w", in.ID, err)
		}
	}
	status, endReason, errText := in.Status()
	req := in.Request
	out := apitypes.Interrogation{
		Id:              in.ID,
		DisplayId:       in.DisplayID(),
		Title:           InterrogationTitle(req),
		Source:          fromInterrogationSource(req.Source),
		Goal:            req.Goal,
		Question:        req.Question,
		TransactionIds:  nonNil(req.TransactionIDs),
		AllowChanges:    nonNil(req.AllowChanges),
		Budget:          fromInterrogationBudget(req.Budget),
		Trigger:         apitypes.InterrogationTrigger{Kind: req.Trigger.Kind, Actor: req.Trigger.Actor},
		Status:          status,
		EndReason:       endReason,
		Error:           errText,
		CreatedAt:       in.CreatedAt,
		ExtendedSeconds: in.Control.ExtendSeconds,
		Stop:            fromInterrogationStop(in),
		Scope:           fromInterrogationScope(st.Scope),
		Progress:        apitypes.InterrogationProgress{Round: st.Round, MaxRounds: req.Budget.MaxRounds},
		ChangeCounts:    fromChangeCounts(in.Changes, deniedProbes(st)),
		Usage: apitypes.InterrogationUsage{
			Rounds:       st.Round,
			ProbesUsed:   in.Changes.Stored,
			MaxProbes:    req.Budget.MaxProbes,
			ModelCalls:   st.ModelCalls,
			InputTokens:  st.InputTokens,
			OutputTokens: st.OutputTokens,
			Model:        st.Model,
		},
		LastSequence: in.Seq,
	}
	if !st.StartedAt.IsZero() {
		out.StartedAt = &st.StartedAt
		deadline := st.StartedAt.Add(time.Duration(req.Budget.TimeLimitSeconds+in.Control.ExtendSeconds) * time.Second)
		out.DeadlineAt = &deadline
	}
	switch {
	case in.Closed != nil:
		out.EndedAt = &in.Closed.At
	case !st.EndedAt.IsZero():
		out.EndedAt = &st.EndedAt
	}
	if out.StartedAt != nil {
		end := now
		if out.EndedAt != nil {
			end = *out.EndedAt
		}
		out.Usage.DurationSeconds = max(0, int(end.Sub(*out.StartedAt)/time.Second))
	}
	if in.Open {
		out.Progress.Node = st.Node
		if w := st.Wait; w != nil {
			out.Progress.Wait = &apitypes.InterrogationWait{Reason: string(w.Reason), Since: w.Since, DeadlineAt: timePtr(w.Until)}
		}
	}
	if st.Answer != nil {
		certainty := st.Answer.Certainty
		out.Certainty = &certainty
	}
	if detail {
		out.Answer = st.Answer
		out.Rounds = make([]apitypes.InterrogationRound, len(st.Rounds))
		for i, r := range st.Rounds {
			out.Rounds[i] = fromInterrogationRound(r)
		}
		out.Changes = runChanges(st, changes, now)
	}
	return out, nil
}

// fromInterrogationStop is the stop recorded in the run's control session, or
// the one that closed it while it was queued.
func fromInterrogationStop(in repo.Interrogation) *apitypes.InterrogationStopRequest {
	switch s, c := in.Control.Stop, in.Closed; {
	case s != nil:
		return &apitypes.InterrogationStopRequest{Changes: apitypes.InterrogationStopRequestChanges(s.Changes), Actor: s.Actor, RequestedAt: s.At}
	case c != nil && c.Reason == repo.InterrogationStoppedByUser:
		return &apitypes.InterrogationStopRequest{Changes: apitypes.InterrogationStopRequestChanges(c.Changes), Actor: c.Actor, RequestedAt: c.At}
	}
	return nil
}

func fromInterrogationRound(r interrogation.Round) apitypes.InterrogationRound {
	nodes := make([]apitypes.InterrogationNode, len(r.Nodes))
	for i, n := range r.Nodes {
		nodes[i] = apitypes.InterrogationNode{Name: n.Name, State: n.State, Visits: n.Visits, EnteredAt: n.EnteredAt, LeftAt: n.LeftAt}
	}
	return apitypes.InterrogationRound{
		Round:         r.Round,
		State:         r.State,
		Outcome:       r.Outcome,
		Final:         r.Final,
		StartedAt:     r.StartedAt,
		EndedAt:       r.EndedAt,
		Nodes:         nodes,
		Observed:      r.Observed,
		Reply:         r.Reply,
		Proposed:      nonNil(r.Proposed),
		Applied:       nonNil(r.Applied),
		ObservedAfter: r.ObservedAfter,
		Timings:       fromTimings(r.Timings),
	}
}

func fromTimings(timings []interrogation.TxTiming) []apitypes.InterrogationTransactionTimings {
	var out []apitypes.InterrogationTransactionTimings
	for _, t := range timings {
		out = append(out, apitypes.InterrogationTransactionTimings{
			TransactionId: int64(t.TransactionID), WindowSeconds: t.WindowSeconds, Requests: t.Requests, Errors: t.Errors,
			AvgMs: float32(t.AvgMs), P95Ms: float32(t.P95Ms), MaxMs: float32(t.MaxMs), AvgSpans: float32(t.AvgSpans), UncoveredAvgMs: float32(t.UncoveredAvgMs),
		})
	}
	return out
}

func fromMeasured(m *interrogation.Measured) *apitypes.InterrogationChangeMeasured {
	if m == nil {
		return nil
	}
	errs := make([]apitypes.InterrogationErrorCount, len(m.TopErrors))
	for i, e := range m.TopErrors {
		errs[i] = apitypes.InterrogationErrorCount{Message: e.Message, Count: e.Count}
	}
	return &apitypes.InterrogationChangeMeasured{
		WindowSeconds: m.WindowSeconds, MeasuredAt: m.MeasuredAt, Spans: m.Spans, Errors: m.Errors,
		AvgMs: float32(m.AvgMs), P95Ms: float32(m.P95Ms), MaxMs: float32(m.MaxMs), SelfAvgMs: float32(m.SelfAvgMs), TopErrors: errs,
	}
}

func fromInterrogationScope(scope []interrogation.ScopeTransaction) []apitypes.InterrogationScopeTransaction {
	out := make([]apitypes.InterrogationScopeTransaction, len(scope))
	for i, s := range scope {
		out[i] = apitypes.InterrogationScopeTransaction{TransactionId: int64(s.TransactionID), Operation: s.Operation, Kind: s.Kind, SelectedBy: s.SelectedBy}
	}
	return out
}

func fromInterrogationSource(s repo.InterrogationSource) apitypes.InterrogationSource {
	return apitypes.InterrogationSource{Namespace: s.Namespace, Kind: s.Kind, Name: s.Name}
}

func fromInterrogationBudget(b repo.InterrogationBudget) apitypes.InterrogationBudget {
	return apitypes.InterrogationBudget{MaxProbes: b.MaxProbes, TimeLimitSeconds: b.TimeLimitSeconds, MaxRounds: b.MaxRounds}
}

func fromChangeCounts(c repo.InterrogationChangeCounts, denied int) apitypes.InterrogationChangeCounts {
	return apitypes.InterrogationChangeCounts{
		Total:        c.Stored + denied,
		Live:         c.Live,
		Reverting:    c.Reverting,
		Reverted:     c.Reverted,
		Expired:      c.Expired,
		Failed:       c.Failed,
		Denied:       denied,
		NextExpiryAt: timePtr(c.NextExpiryAt),
	}
}

// deniedProbes counts the proposals the run refused. They are not stored as
// changes.
func deniedProbes(st interrogation.State) int {
	n := 0
	for _, p := range st.Probes {
		if deniedOnly(p) {
			n++
		}
	}
	return n
}

// deniedOnly is whether a probe exists only as a denied proposal. The loop
// denies a probe as already requested when the run stored a change under the
// same id before (an apply repeated after its row ended); that stored change
// is the one listed and counted.
func deniedOnly(p interrogation.Probe) bool {
	return p.Status == interrogation.ProbeDenied && p.DenyReason != string(loop.DenyAlreadyRequested)
}

// InterrogationList maps a page of runs to the wire list.
func InterrogationList(runs []repo.Interrogation, now time.Time) (apitypes.InterrogationList, error) {
	items := make([]apitypes.Interrogation, len(runs))
	for i, in := range runs {
		var err error
		if items[i], err = FromInterrogation(in, nil, false, now); err != nil {
			return apitypes.InterrogationList{}, err
		}
	}
	return apitypes.InterrogationList{Count: len(items), Items: items}, nil
}

// storedEvent is an event as the workflow stored it. Its payload is served as
// stored.
type storedEvent struct {
	interrogation.Event
	Payload json.RawMessage `json:"payload"`
}

func decodeEvent(ev repo.InterrogationEvent) (storedEvent, error) {
	var s storedEvent
	if err := json.Unmarshal(ev.JSON, &s); err != nil {
		return storedEvent{}, fmt.Errorf("decode interrogation event %d: %w", ev.Seq, err)
	}
	return s, nil
}

// InterrogationEventList maps a page of a run's event log to the wire list.
func InterrogationEventList(page repo.InterrogationEvents) (apitypes.InterrogationEventList, error) {
	items := make([]apitypes.InterrogationEvent, len(page.Items))
	for i, ev := range page.Items {
		s, err := decodeEvent(ev)
		if err != nil {
			return apitypes.InterrogationEventList{}, err
		}
		payload := s.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		items[i] = apitypes.InterrogationEvent{
			Sequence: s.Seq,
			Type:     s.Type,
			Time:     s.Time,
			Round:    s.Round,
			Node:     s.Node,
			ChangeId: s.ChangeID,
			Message:  s.Message,
			Payload:  payload,
		}
	}
	return apitypes.InterrogationEventList{
		Count:        len(items),
		Items:        items,
		LastSequence: page.LastSequence,
		Complete:     page.Complete,
	}, nil
}

// runChanges lists every change of a run at now, oldest first: its stored
// changes and the proposals it denied, which exist only in its state.
func runChanges(st interrogation.State, stored []repo.InterrogationChange, now time.Time) []apitypes.InterrogationChange {
	measured := map[string]*interrogation.Measured{}
	for _, p := range st.Probes {
		measured[p.ChangeID] = p.Measured
	}
	out := make([]apitypes.InterrogationChange, 0, len(stored))
	for _, c := range stored {
		ch := fromChange(c, now)
		ch.Measured = fromMeasured(measured[c.ChangeID])
		out = append(out, ch)
	}
	for _, p := range st.Probes {
		if deniedOnly(p) {
			out = append(out, deniedChange(p))
		}
	}
	slices.SortStableFunc(out, func(a, b apitypes.InterrogationChange) int {
		return a.RequestedAt.Compare(b.RequestedAt)
	})
	return out
}

func fromChange(c repo.InterrogationChange, now time.Time) apitypes.InterrogationChange {
	state := c.State(now)
	out := apitypes.InterrogationChange{
		Id:       c.ChangeID,
		Round:    c.Round,
		Language: c.Language,
		Target: apitypes.InterrogationChangeTarget{
			ClassName:          c.ClassName,
			MethodName:         c.MethodName,
			PackageName:        c.PackageName,
			FunctionName:       c.FunctionName,
			ReceiverName:       c.ReceiverName,
			ReceiverMethodName: c.ReceiverMethodName,
			Signature:          c.Signature,
		},
		State:       state,
		Checks:      loop.PassedChecks(""),
		RequestedAt: c.FirstSeen,
		AppliedAt:   timePtr(c.AppliedAt),
		ExpiresAt:   timePtr(c.ExpiresAt),
	}
	if state == repo.ChangeBlocked {
		out.BlockReason = c.BlockReason
	}
	switch {
	case state == repo.ChangeExpired:
		out.EndReason = repo.ChangeExpired
		out.EndedAt = timePtr(c.ExpiresAt)
	case state == repo.ChangeReverted, state == repo.ChangeFailed:
		out.EndReason = c.EndReason
		out.EndedAt = timePtr(c.UpdatedAt)
	case state == repo.ChangeReverting && c.Desired == repo.ChangeApproved:
		out.EndReason = repo.ChangeExpired
	case state == repo.ChangeReverting:
		out.EndReason = c.EndReason
	}
	if !c.RemovedAt.IsZero() && out.EndedAt != nil {
		out.EndedAt = &c.RemovedAt
	}
	return out
}

func deniedChange(p interrogation.Probe) apitypes.InterrogationChange {
	return apitypes.InterrogationChange{
		Id:       p.ChangeID,
		Round:    p.Round,
		Language: p.Language,
		Target: apitypes.InterrogationChangeTarget{
			ClassName:          p.Rec.Probe.ClassName,
			MethodName:         p.Rec.Probe.MethodName,
			PackageName:        p.Rec.Probe.PackageName,
			FunctionName:       p.Rec.Probe.FunctionName,
			ReceiverName:       p.Rec.Probe.ReceiverName,
			ReceiverMethodName: p.Rec.Probe.ReceiverMethodName,
			Signature:          p.Rec.Probe.Signature,
		},
		State:       string(interrogation.ProbeDenied),
		DenyReason:  p.DenyReason,
		Checks:      loop.PassedChecks(loop.DenyReason(p.DenyReason)),
		RequestedAt: p.ProposedAt,
	}
}

// FromInterrogationChangesSummary maps the live changes' totals to the wire
// type. maxProbes is the per-workload cap.
func FromInterrogationChangesSummary(s repo.InterrogationChangesSummary, maxProbes int) apitypes.InterrogationChangesSummary {
	byKind := map[string]int{}
	if s.Live > 0 {
		byKind[changeKind] = s.Live
	}
	bySource := make([]apitypes.InterrogationChangesBySource, len(s.BySource))
	for i, b := range s.BySource {
		bySource[i] = apitypes.InterrogationChangesBySource{
			Source:    fromInterrogationSource(b.Source),
			Language:  b.Language,
			Live:      b.Live,
			Probes:    b.Live,
			MaxProbes: maxProbes,
		}
	}
	return apitypes.InterrogationChangesSummary{
		Live:               s.Live,
		ByKind:             byKind,
		NextExpiryAt:       timePtr(s.NextExpiryAt),
		NextExpiryChangeId: s.NextExpiryChangeID,
		BySource:           bySource,
	}
}

// FromInterrogationSummary maps the list page's counters to the wire type.
func FromInterrogationSummary(s repo.InterrogationSummary, changes repo.InterrogationChangesSummary, maxProbes int) apitypes.InterrogationSummary {
	running := make([]apitypes.InterrogationSource, len(s.RunningSources))
	for i, src := range s.RunningSources {
		running[i] = fromInterrogationSource(src)
	}
	out := apitypes.InterrogationSummary{
		Queued:         s.Queued,
		Running:        s.Running,
		RunningSources: running,
		Total:          s.Total,
		Concluded:      s.Ended,
		Changes:        FromInterrogationChangesSummary(changes, maxProbes),
		ModelUsage: apitypes.InterrogationModelUsage{
			Calls:        int(s.ModelCalls),
			InputTokens:  s.InputTokens,
			OutputTokens: s.OutputTokens,
			Model:        s.Model,
			Gateway:      gateway,
		},
	}
	if s.Averaged > 0 {
		rounds, seconds := float32(s.AvgRounds), int(s.AvgDurationSeconds)
		out.AvgRounds, out.AvgDurationSeconds = &rounds, &seconds
	}
	return out
}

// InterrogationSourceList maps the workloads with evidence to the picker's
// list. maxProbes is the per-workload cap.
func InterrogationSourceList(sources []repo.InterrogationSourceStat, maxProbes int) apitypes.InterrogationSourceList {
	items := make([]apitypes.InterrogationPickerSource, len(sources))
	for i, s := range sources {
		items[i] = apitypes.InterrogationPickerSource{
			Namespace:    s.Source.Namespace,
			Kind:         s.Source.Kind,
			Name:         s.Source.Name,
			Language:     sourceLanguage,
			Transactions: s.Transactions,
			LastSeen:     timePtr(s.LastSeen),
			Busy:         s.OpenID != "",
			Probes:       s.LiveChanges,
			MaxProbes:    maxProbes,
		}
		if s.OpenID != "" {
			items[i].ActiveInterrogationId = s.OpenID
			items[i].ActiveDisplayId = repo.Interrogation{ID: s.OpenID}.DisplayID()
		}
	}
	return apitypes.InterrogationSourceList{Count: len(items), Items: items}
}

// InterrogationSourceTransactionList maps a workload's transactions to the
// Focus list.
func InterrogationSourceTransactionList(txs []repo.InterrogationSourceTransaction) apitypes.InterrogationSourceTransactionList {
	items := make([]apitypes.InterrogationSourceTransaction, len(txs))
	for i, tx := range txs {
		items[i] = apitypes.InterrogationSourceTransaction{
			TransactionId: tx.TransactionID,
			Operation:     tx.Operation,
			Kind:          tx.Kind,
			Volume:        tx.Volume,
			LastSeen:      timePtr(tx.LastSeen),
			Functions:     tx.Functions,
		}
	}
	return apitypes.InterrogationSourceTransactionList{Count: len(items), Items: items}
}

// timePtr is t, or nil for an unset time, which the wire omits.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// nonNil is s, or an empty slice for nil, so it is written as [] and not
// null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
