package interrogation

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestProbeOutcome(t *testing.T) {
	now := testStart.Add(time.Minute)
	live := repo.InterrogationChange{Desired: repo.ChangeApproved, ExpiresAt: now.Add(time.Hour)}
	with := func(edit func(*repo.InterrogationChange)) repo.InterrogationChange {
		ch := live
		edit(&ch)
		return ch
	}
	tests := []struct {
		name       string
		ch         repo.InterrogationChange
		missing    bool
		late       bool
		want       outcome
		wantReason string
	}{
		{name: "in rule", ch: with(func(c *repo.InterrogationChange) { c.InRule = true }), want: probeInRule},
		{name: "in rule when late", ch: with(func(c *repo.InterrogationChange) { c.InRule = true }), late: true, want: probeInRule},
		{name: "covered by another rule", ch: with(func(c *repo.InterrogationChange) { c.BlockReason = loop.BlockCoveredByRule }), want: probeInRule},
		{name: "blocked", ch: with(func(c *repo.InterrogationChange) { c.BlockReason = loop.BlockRuleDisabled }), want: probeGivenUp, wantReason: loop.BlockRuleDisabled},
		{name: "blocked when late", ch: with(func(c *repo.InterrogationChange) { c.BlockReason = loop.BlockWorkloadCap }), late: true, want: probeGivenUp, wantReason: loop.BlockWorkloadCap},
		{name: "gone", missing: true, want: probeGivenUp, wantReason: repo.ChangeNotAppliedInTime},
		{name: "late", ch: live, late: true, want: probeGivenUp, wantReason: repo.ChangeNotAppliedInTime},
		{name: "expired with the run", ch: with(func(c *repo.InterrogationChange) { c.ExpiresAt = now }), want: probeGivenUp, wantReason: repo.ChangeNotAppliedInTime},
		{name: "source gone", ch: with(func(c *repo.InterrogationChange) { c.Desired, c.EndReason = repo.ChangeReverted, "source_gone" }), want: probeEnded, wantReason: "source_gone"},
		{name: "source gone, still in the rule", ch: with(func(c *repo.InterrogationChange) {
			c.Desired, c.EndReason, c.InRule = repo.ChangeReverted, "source_gone", true
		}), want: probeEnded, wantReason: "source_gone"},
		{name: "waiting", ch: live, want: probeWaiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := probeOutcome(tt.ch, !tt.missing, tt.late, now)
			if got != tt.want || reason != tt.wantReason {
				t.Fatalf("probeOutcome = %d, %q; want %d, %q", got, reason, tt.want, tt.wantReason)
			}
		})
	}
}

// listOnly is a Changes that only lists rows.
type listOnly struct {
	Changes
	rows  []repo.InterrogationChange
	lists int
}

func (l *listOnly) ListInterrogationChanges(context.Context, string) ([]repo.InterrogationChange, error) {
	l.lists++
	return l.rows, nil
}

func TestSyncChanges(t *testing.T) {
	now := testStart.Add(10 * time.Minute)
	st := runningState()
	probe := func(id string, status ProbeStatus) Probe {
		return Probe{ChangeID: id, Language: apschema.LangJava, Target: "a.B." + id, Round: 1, Status: status}
	}
	st.Probes = []Probe{
		probe("source-gone", ProbeApplied),
		probe("expired", ProbeRequested),
		probe("live", ProbeApplied),
		probe("denied", ProbeDenied),
		probe("failed", ProbeFailed),
		probe("no-row", ProbeRequested),
	}
	changes := &listOnly{rows: []repo.InterrogationChange{
		{ChangeID: "source-gone", Desired: repo.ChangeReverted, EndReason: "source_gone"},
		{ChangeID: "expired", Desired: repo.ChangeApproved, ExpiresAt: now},
		{ChangeID: "live", Desired: repo.ChangeApproved, ExpiresAt: now.Add(time.Hour)},
		{ChangeID: "failed", Desired: repo.ChangeReverted, EndReason: repo.ChangeNotAppliedInTime},
	}}
	evs, err := syncChanges(context.Background(), changes, st, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(evs); !slices.Equal(got, []string{EventChangeReverted, EventChangeReverted}) {
		t.Fatalf("events %v, want two change.reverted", got)
	}
	if evs[0].ChangeID != "source-gone" || evs[0].Payload["end_reason"] != "source_gone" {
		t.Errorf("first event %+v, want the source gone", evs[0])
	}
	if evs[1].ChangeID != "expired" || evs[1].Payload["end_reason"] != repo.ChangeExpired {
		t.Errorf("second event %+v, want the expiry", evs[1])
	}
	want := []ProbeStatus{ProbeReverted, ProbeReverted, ProbeApplied, ProbeDenied, ProbeFailed, ProbeRequested}
	for i, p := range st.Probes {
		if p.Status != want[i] {
			t.Errorf("probe %s: %s, want %s", p.ChangeID, p.Status, want[i])
		}
	}

	// Nothing to sync: the rows are not read.
	changes.lists = 0
	st.Probes = []Probe{probe("denied", ProbeDenied)}
	if evs, err := syncChanges(context.Background(), changes, st, now); err != nil || len(evs) != 0 || changes.lists != 0 {
		t.Fatalf("events %v, error %v, %d lists; want no read", evs, err, changes.lists)
	}
}

func TestProposeProbes(t *testing.T) {
	rec := func(class, method string) apschema.Recommendation {
		r := validReply().Recommendations[0]
		r.Function, r.Probe = class+"."+method, apschema.Probe{ClassName: class, MethodName: method}
		return r
	}
	validate, find := rec("com.claims.IntakeService", "validate"), rec("com.claims.PolicyRepository", "find")
	tests := []struct {
		name        string
		edit        func(st *State)
		recs        []apschema.Recommendation
		wantPending int
		wantEvents  []string
		wantDeny    loop.DenyReason
	}{
		{
			name:        "pending",
			recs:        []apschema.Recommendation{validate, find},
			wantPending: 2,
			wantEvents:  []string{EventChangeProposed, EventChangeProposed},
		},
		{
			name:        "twice in one reply",
			recs:        []apschema.Recommendation{validate, validate},
			wantPending: 1,
			wantEvents:  []string{EventChangeProposed},
		},
		{
			name: "proposed in an earlier round",
			edit: func(st *State) {
				st.Probes = []Probe{{ChangeID: loop.ChangeID(st.ID, apschema.LangJava, "com.claims.IntakeService.validate"), Round: 1, Status: ProbeApplied}}
				st.Round = 2
				st.Rounds = append(st.Rounds, Round{Round: 2})
			},
			recs:        []apschema.Recommendation{validate, find},
			wantPending: 1,
			wantEvents:  []string{EventChangeDenied, EventChangeProposed},
		},
		{
			name:       "no opt-in",
			edit:       func(st *State) { st.Request.AllowChanges = nil },
			recs:       []apschema.Recommendation{validate},
			wantEvents: []string{EventChangeProposed, EventChangeDenied},
			wantDeny:   loop.DenyChangesNotAllowed,
		},
		{
			name:       "final round",
			edit:       func(st *State) { st.Final = true },
			recs:       []apschema.Recommendation{validate},
			wantEvents: []string{EventChangeProposed, EventChangeDenied},
			wantDeny:   loop.DenyFinalRound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := runningState()
			if tt.edit != nil {
				tt.edit(st)
			}
			before := len(st.Probes)
			pending, evs := proposeProbes(st, tt.recs, testStart)
			if pending != tt.wantPending || !slices.Equal(eventTypes(evs), tt.wantEvents) {
				t.Fatalf("pending %d, events %v; want %d, %v", pending, eventTypes(evs), tt.wantPending, tt.wantEvents)
			}
			added := st.Probes[before:]
			var ids []string
			for _, p := range added {
				if p.ChangeID != loop.ChangeID(st.ID, p.Language, p.Target) || p.Round != st.Round {
					t.Errorf("probe %+v: change id or round", p)
				}
				if tt.wantDeny != "" && (p.Status != ProbeDenied || p.DenyReason != string(tt.wantDeny)) {
					t.Errorf("probe %s/%s, want denied %s", p.Status, p.DenyReason, tt.wantDeny)
				}
				ids = append(ids, p.ChangeID)
			}
			if r := st.currentRound(); !slices.Equal(r.Proposed, ids) {
				t.Errorf("round proposed %v, want %v", r.Proposed, ids)
			}
		})
	}
}
