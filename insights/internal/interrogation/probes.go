package interrogation

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// endReasonStop is the end reason of a probe a stop with revert removed.
const endReasonStop = "stop"

// What confirm makes of a requested probe's row.
type outcome int

const (
	probeWaiting outcome = iota
	// probeInRule: the rule has it, from this row or from another rule.
	probeInRule
	// probeEnded: the row ended meanwhile: a person reverted it, or its
	// source is gone.
	probeEnded
	// probeGivenUp: blocked, gone, or not in the rule in time.
	probeGivenUp
)

// proposeProbes records the probes the model recommended in this round. What
// needs no lookup is denied here: a run that did not opt in to changes, and
// the final round. Every other probe is pending until apply asks the loop. A
// probe the run already proposed is not recorded again: in this reply it is
// skipped, from an earlier round it is denied as already requested. It
// returns how many are pending and their events.
func proposeProbes(st *State, recs []apschema.Recommendation, t time.Time) (int, []Event) {
	pending := 0
	var evs []Event
	r := st.currentRound()
	for _, rec := range recs {
		target := loop.ProbeTarget(rec)
		id := loop.ChangeID(st.ID, rec.Language, target)
		if i := slices.IndexFunc(st.Probes, func(p Probe) bool { return p.ChangeID == id }); i >= 0 {
			if st.Probes[i].Round < st.Round {
				// The event reports this round's ask; the run's probe keeps its state.
				again := st.Probes[i]
				again.Round, again.DenyReason, again.Detail = st.Round, string(loop.DenyAlreadyRequested), ""
				evs = append(evs, changeDenied(st, &again, t))
			}
			continue
		}
		st.Probes = append(st.Probes, Probe{
			ChangeID:   id,
			Language:   rec.Language,
			Target:     target,
			Category:   rec.Category,
			Why:        rec.Why,
			Rec:        rec,
			Round:      st.Round,
			Status:     ProbePending,
			ProposedAt: t,
		})
		p := &st.Probes[len(st.Probes)-1]
		r.Proposed = append(r.Proposed, id)
		evs = append(evs, changeProposed(st, p, t))
		switch {
		case !allowsChanges(st):
			evs = append(evs, st.deny(p, loop.DenyChangesNotAllowed, "", t))
		case st.Final:
			evs = append(evs, st.deny(p, loop.DenyFinalRound, "", t))
		default:
			pending++
		}
	}
	return pending, evs
}

// probeRequest is what apply asks the loop for one pending probe. The expiry
// is not passed: RequestProbe takes it from the run's sessions under their
// row locks.
func probeRequest(st *State, p *Probe) loop.ProbeRequest {
	txs := make([]loop.ProbeTransaction, 0, len(st.Transactions))
	for _, s := range st.Transactions {
		txs = append(txs, loop.ProbeTransaction{ContainerName: s.Container, TransactionID: s.ID})
	}
	return loop.ProbeRequest{
		InterrogationID: st.ID,
		Source:          workloadSource(st),
		Transactions:    txs,
		Round:           p.Round,
		Revision:        st.Revision,
		Recommendation:  p.Rec,
		ChangesAllowed:  allowsChanges(st),
		RunMaxProbes:    st.Request.Budget.MaxProbes,
	}
}

// probeOutcome reads what the reconciler made of a requested probe's row.
// late is whether confirm waited long enough or the run's deadline passed;
// reason is the end reason of an ended or given up probe. A row that ended is
// ended for the run too, also when the reconciler has not taken it out of the
// rule yet.
func probeOutcome(ch repo.InterrogationChange, found, late bool, now time.Time) (outcome, string) {
	switch {
	case !found:
		return probeGivenUp, repo.ChangeNotAppliedInTime
	case ch.Desired == repo.ChangeReverted:
		return probeEnded, ch.EndReason
	case ch.InRule, ch.BlockReason == loop.BlockCoveredByRule:
		return probeInRule, ""
	case ch.BlockReason != "":
		return probeGivenUp, ch.BlockReason
	case late, !ch.Live(now):
		// A probe expires with its run's deadline, which is also when
		// confirm stops waiting.
		return probeGivenUp, repo.ChangeNotAppliedInTime
	}
	return probeWaiting, ""
}

// syncChanges marks the run's live probes whose rows ended since it last
// looked: a person reverted them, a stop with revert did, their source is
// gone, or they expired. The model is then no longer told they are in place.
// A kept probe is live and stays as it is.
func syncChanges(ctx context.Context, changes Changes, st *State, t time.Time) ([]Event, error) {
	if !slices.ContainsFunc(st.Probes, func(p Probe) bool { return probeLive(p.Status) }) {
		return nil, nil
	}
	rows, err := changes.ListInterrogationChanges(ctx, st.ID)
	if err != nil {
		return nil, fmt.Errorf("list changes of %s: %w", st.ID, err)
	}
	byID := make(map[string]repo.InterrogationChange, len(rows))
	for _, ch := range rows {
		byID[ch.ChangeID] = ch
	}
	var evs []Event
	for i := range st.Probes {
		p := &st.Probes[i]
		ch, ok := byID[p.ChangeID]
		if !probeLive(p.Status) || !ok || ch.Live(t) {
			continue
		}
		by := ""
		if ch.EndReason == endReasonStop && st.Stop != nil {
			by = st.Stop.Actor
		}
		evs = append(evs, st.reverted(p, cmp.Or(ch.EndReason, repo.ChangeExpired), by, t))
	}
	return evs, nil
}

// requestStored marks the run's pending probes that have a row as requested.
// An apply that failed after storing some of its probes left them pending,
// and the reconciler places them all the same.
func requestStored(ctx context.Context, changes Changes, st *State, t time.Time) ([]Event, error) {
	if !slices.ContainsFunc(st.Probes, func(p Probe) bool { return p.Status == ProbePending }) {
		return nil, nil
	}
	rows, err := changes.ListInterrogationChanges(ctx, st.ID)
	if err != nil {
		return nil, fmt.Errorf("list changes of %s: %w", st.ID, err)
	}
	var evs []Event
	for _, ch := range rows {
		i := slices.IndexFunc(st.Probes, func(p Probe) bool { return p.ChangeID == ch.ChangeID })
		if i >= 0 && st.Probes[i].Status == ProbePending {
			evs = append(evs, st.requested(&st.Probes[i], ch.ExpiresAt, t))
		}
	}
	return evs, nil
}

// probeLive is whether the run counts a probe as in place or on its way.
func probeLive(s ProbeStatus) bool {
	return s == ProbeRequested || s == ProbeApplied
}

// probeBudgetLeft is how many more probes the run may request: every probe
// that got a row counts, a denied proposal does not.
func probeBudgetLeft(st *State) int {
	used := 0
	for _, p := range st.Probes {
		if p.Status != ProbeDenied && p.Status != ProbePending {
			used++
		}
	}
	return max(st.Request.Budget.MaxProbes-used, 0)
}

// probesIn returns the run's probes of round in status s.
func (st *State) probesIn(round int, s ProbeStatus) []*Probe {
	var out []*Probe
	for i := range st.Probes {
		if p := &st.Probes[i]; p.Round == round && p.Status == s {
			out = append(out, p)
		}
	}
	return out
}

func (st *State) deny(p *Probe, reason loop.DenyReason, detail string, t time.Time) Event {
	p.Status, p.DenyReason, p.Detail, p.EndedAt = ProbeDenied, string(reason), detail, t
	return changeDenied(st, p, t)
}

func (st *State) requested(p *Probe, expiresAt, t time.Time) Event {
	p.Status, p.RequestedAt, p.ExpiresAt = ProbeRequested, t, expiresAt
	return changeRequested(st, p, t)
}

func (st *State) applied(p *Probe, ch repo.InterrogationChange, ruleName string, t time.Time) Event {
	p.Status, p.AppliedAt, p.ExpiresAt = ProbeApplied, t, ch.ExpiresAt
	if r := st.currentRound(); r != nil {
		r.Applied = append(r.Applied, p.ChangeID)
	}
	return changeApplied(st, p, ruleName, t)
}

func (st *State) failed(p *Probe, reason string, t time.Time) Event {
	p.Status, p.EndReason, p.EndedAt = ProbeFailed, reason, t
	return changeFailed(st, p, t)
}

func (st *State) reverted(p *Probe, reason, by string, t time.Time) Event {
	p.Status, p.EndReason, p.EndedAt = ProbeReverted, reason, t
	return changeReverted(st, p, by, t)
}
