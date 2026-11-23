package interrogation

import (
	"context"
	"slices"
	"testing"
	"time"
)

// runningState is a run picked up at testStart with an hour's limit, in
// round 1 at gather.
func runningState() *State {
	st := newScript().st
	st.Status, st.StartedAt, st.DeadlineAt = StatusRunning, testStart, testStart.Add(time.Hour)
	st.Step, st.Node, st.Round = StepGate, "gather", 1
	st.Rounds = []Round{{Round: 1, State: RoundInProgress, Nodes: []NodeRun{{Name: "gather", State: NodeRunning, Visits: 1, EnteredAt: testStart}}}}
	return st
}

func eventTypes(evs []Event) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestApplyControl(t *testing.T) {
	now := testStart.Add(10 * time.Minute)
	keep := &Stop{Changes: ChangesKeep, Actor: "alice", At: now}
	revert := &Stop{Changes: ChangesRevert, Actor: "bob", At: now.Add(time.Second)}

	tests := []struct {
		name string
		// before changes the running state; each control is applied in turn.
		before   func(*State)
		controls []Control
		// The result of the last control.
		wantEvents   []string
		wantConclude bool
		check        func(t *testing.T, st *State)
	}{
		{
			name:         "nothing to apply",
			controls:     []Control{{}},
			wantEvents:   []string{},
			wantConclude: false,
		},
		{
			name:         "fail",
			controls:     []Control{{Fail: "graph version 2, running 1"}},
			wantEvents:   []string{},
			wantConclude: true,
			check: func(t *testing.T, st *State) {
				if st.EndReason != EndInterrupted || st.Error != "graph version 2, running 1" || st.terminalStatus() != StatusFailed {
					t.Fatalf("ended %s %q", st.EndReason, st.Error)
				}
			},
		},
		{
			name:         "stop",
			controls:     []Control{{Stop: keep}},
			wantEvents:   []string{EventStopRequested},
			wantConclude: true,
			check: func(t *testing.T, st *State) {
				if st.Stop == nil || *st.Stop != *keep || st.EndReason != EndStoppedByUser || st.terminalStatus() != StatusStopped {
					t.Fatalf("stop %+v, ended %s", st.Stop, st.EndReason)
				}
			},
		},
		{
			name:         "stop delivered twice",
			controls:     []Control{{Stop: keep}, {Stop: keep}},
			wantEvents:   []string{},
			wantConclude: true,
		},
		{
			name:         "revert overrides keep",
			controls:     []Control{{Stop: keep}, {Stop: revert}},
			wantEvents:   []string{},
			wantConclude: true,
			check: func(t *testing.T, st *State) {
				if st.Stop.Changes != ChangesRevert || st.Stop.Actor != "alice" {
					t.Fatalf("stop = %+v, want alice's stop now reverting", st.Stop)
				}
			},
		},
		{
			name:         "keep does not override revert",
			controls:     []Control{{Stop: revert}, {Stop: keep}},
			wantEvents:   []string{},
			wantConclude: true,
			check: func(t *testing.T, st *State) {
				if st.Stop.Changes != ChangesRevert {
					t.Fatalf("stop = %+v, want revert", st.Stop)
				}
			},
		},
		{
			name:         "stop while concluding for another reason",
			before:       func(st *State) { st.end(EndDone, "") },
			controls:     []Control{{Stop: revert}},
			wantEvents:   []string{EventStopRequested},
			wantConclude: true,
			check: func(t *testing.T, st *State) {
				if st.EndReason != EndDone || st.Stop == nil {
					t.Fatalf("ended %s with stop %+v, want done with the stop recorded", st.EndReason, st.Stop)
				}
			},
		},
		{
			name:         "extension",
			controls:     []Control{{ExtendedSeconds: 600}},
			wantEvents:   []string{EventExtended},
			wantConclude: false,
			check: func(t *testing.T, st *State) {
				if want := testStart.Add(time.Hour + 10*time.Minute); !st.DeadlineAt.Equal(want) {
					t.Fatalf("deadline %v, want %v", st.DeadlineAt, want)
				}
			},
		},
		{
			name:         "extension delivered twice",
			controls:     []Control{{ExtendedSeconds: 600}, {ExtendedSeconds: 600}},
			wantEvents:   []string{},
			wantConclude: false,
			check: func(t *testing.T, st *State) {
				if want := testStart.Add(time.Hour + 10*time.Minute); !st.DeadlineAt.Equal(want) {
					t.Fatalf("deadline %v, want %v", st.DeadlineAt, want)
				}
			},
		},
		{
			name:         "second extension",
			controls:     []Control{{ExtendedSeconds: 600}, {ExtendedSeconds: 900}},
			wantEvents:   []string{EventExtended},
			wantConclude: false,
		},
		{
			name: "extension of a run not started",
			before: func(st *State) {
				st.StartedAt, st.DeadlineAt = time.Time{}, time.Time{}
			},
			controls:     []Control{{ExtendedSeconds: 600}},
			wantEvents:   []string{},
			wantConclude: false,
		},
		{
			name:         "first deadline",
			before:       func(st *State) { st.DeadlineAt = time.Time{} },
			controls:     []Control{{ExtendedSeconds: 600}},
			wantEvents:   []string{},
			wantConclude: false,
			check: func(t *testing.T, st *State) {
				if want := testStart.Add(time.Hour + 10*time.Minute); !st.DeadlineAt.Equal(want) {
					t.Fatalf("deadline %v, want %v", st.DeadlineAt, want)
				}
			},
		},
		{
			name:         "resume after an error",
			controls:     []Control{{Resume: &Resume{Error: "connection refused", RetryAfter: 5}}},
			wantEvents:   []string{EventErrorRaised},
			wantConclude: false,
			check: func(t *testing.T, st *State) {
				if st.StepFailures != 1 || st.LastError != "connection refused" {
					t.Fatalf("step failures %d, last error %q", st.StepFailures, st.LastError)
				}
				if n := st.Rounds[0].Nodes[0]; n.State != NodeFailed || n.LeftAt == nil {
					t.Fatalf("gather = %+v, want failed", n)
				}
			},
		},
		{
			name:         "resume after a model error",
			controls:     []Control{{Resume: &Resume{Error: "429 Too Many Requests", ModelCall: true}}},
			wantEvents:   []string{EventErrorRaised, EventModelCalled},
			wantConclude: false,
		},
		{
			name:         "a resume with no error",
			controls:     []Control{{Resume: &Resume{}}},
			wantEvents:   []string{},
			wantConclude: false,
		},
		{
			name:         "failures below the limit",
			controls:     repeat(Control{Resume: &Resume{Error: "e"}}, maxStepFailures-1),
			wantEvents:   []string{EventErrorRaised},
			wantConclude: false,
			check: func(t *testing.T, st *State) {
				if st.StepFailures != maxStepFailures-1 || st.EndReason != "" {
					t.Fatalf("step failures %d, ended %q", st.StepFailures, st.EndReason)
				}
			},
		},
		{
			name:         "failures reach the limit",
			controls:     repeat(Control{Resume: &Resume{Error: "e"}}, maxStepFailures),
			wantEvents:   []string{EventErrorRaised},
			wantConclude: true,
			check: func(t *testing.T, st *State) {
				if st.EndReason != EndError || st.Error != "gate failed 5 times in a row: e" {
					t.Fatalf("ended %s %q", st.EndReason, st.Error)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := runningState()
			if tt.before != nil {
				tt.before(st)
			}
			var evs []Event
			var conclude bool
			for _, ctl := range tt.controls {
				evs, conclude = applyControl(st, ctl, now)
			}
			if got := eventTypes(evs); !slices.Equal(got, tt.wantEvents) {
				t.Fatalf("events %v, want %v", got, tt.wantEvents)
			}
			if conclude != tt.wantConclude {
				t.Fatalf("conclude %v, want %v", conclude, tt.wantConclude)
			}
			if tt.check != nil {
				tt.check(t, st)
			}
		})
	}
}

func TestApplyControl_FailuresResetWhenTheStepChanges(t *testing.T) {
	st := runningState()
	fail := Control{Resume: &Resume{Error: "e"}}
	for range maxStepFailures - 1 {
		applyControl(st, fail, testStart)
	}
	st.at = "record"
	emit(testInvocation{context.Background()}, st, testStart, StepApply, "apply")
	if st.StepFailures != 0 {
		t.Fatalf("step failures %d after the step changed, want 0", st.StepFailures)
	}
	if _, conclude := applyControl(st, fail, testStart); conclude || st.StepFailures != 1 {
		t.Fatalf("conclude %v with %d failures, want the count started again", conclude, st.StepFailures)
	}

	// A node event at the same step is not a success of the step.
	st.at = "apply"
	emit(testInvocation{context.Background()}, st, testStart, StepApply, "wait")
	if st.StepFailures != 1 {
		t.Fatalf("step failures %d after an event at the same step, want 1", st.StepFailures)
	}
}

func repeat(c Control, n int) []Control {
	out := make([]Control, n)
	for i := range out {
		out[i] = c
	}
	return out
}
