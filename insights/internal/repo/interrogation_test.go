package repo

import (
	"testing"
	"time"
)

// One case per status rule, in the order Status applies them.
func TestInterrogation_Status(t *testing.T) {
	stop := &InterrogationStop{Changes: "revert", Actor: "alice"}
	cases := []struct {
		name                    string
		in                      Interrogation
		status, reason, errText string
	}{
		{"open with no workflow state", Interrogation{Open: true, Control: InterrogationControl{Stop: stop}}, "queued", "", ""},
		{"open with a stop recorded", Interrogation{Open: true, Started: true, RunStatus: "running", Control: InterrogationControl{Stop: stop}}, "stopping", "", ""},
		{"open", Interrogation{Open: true, Started: true, RunStatus: "running"}, "running", "", ""},
		{"stopped while queued", Interrogation{Closed: &InterrogationClosed{Reason: "stopped_by_user"}}, "stopped", "stopped_by_user", ""},
		{"closed overdue after a stop", Interrogation{Started: true, RunStatus: "running",
			Closed: &InterrogationClosed{Reason: "stopped_by_user"}, Control: InterrogationControl{Stop: stop}}, "stopped", "stopped_by_user", ""},
		{"closed without the workflow", Interrogation{Started: true, RunStatus: "running", RunError: "store down",
			Closed: &InterrogationClosed{Reason: "interrupted", Error: "did not finish by its deadline"}}, "failed", "interrupted", "did not finish by its deadline"},
		{"the workflow's own ending", Interrogation{Started: true, RunStatus: "completed", RunEndReason: "done",
			Control: InterrogationControl{Stop: stop}}, "completed", "done", ""},
		{"the workflow's own failure", Interrogation{Started: true, RunStatus: "failed", RunEndReason: "error", RunError: "boom"}, "failed", "error", "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason, errText := tc.in.Status()
			if status != tc.status || reason != tc.reason || errText != tc.errText {
				t.Errorf("Status() = %q, %q, %q; want %q, %q, %q", status, reason, errText, tc.status, tc.reason, tc.errText)
			}
		})
	}
}

// One case per stored row shape State derives a wire state from; a denied
// proposal has no row and is the workflow's to report.
func TestInterrogationChange_State(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	cases := []struct {
		name  string
		c     InterrogationChange
		state string
	}{
		{"live, not in the rule, not blocked", InterrogationChange{Desired: ChangeApproved, ExpiresAt: later}, "requested"},
		{"live, not in the rule, blocked", InterrogationChange{Desired: ChangeApproved, ExpiresAt: later, BlockReason: "rule_disabled"}, "blocked"},
		{"live, in the rule", InterrogationChange{Desired: ChangeApproved, ExpiresAt: later, InRule: true}, "active"},
		{"not live, in the rule", InterrogationChange{Desired: ChangeReverted, EndReason: "stop", InRule: true}, "reverting"},
		{"expired, still in the rule", InterrogationChange{Desired: ChangeApproved, ExpiresAt: earlier, InRule: true}, "reverting"},
		{"not applied in time", InterrogationChange{Desired: ChangeReverted, EndReason: "not_applied_in_time"}, "failed"},
		{"given up as blocked", InterrogationChange{Desired: ChangeReverted, EndReason: "workload_cap_reached"}, "failed"},
		{"reverted", InterrogationChange{Desired: ChangeReverted, EndReason: "stop"}, "reverted"},
		{"expired", InterrogationChange{Desired: ChangeApproved, ExpiresAt: earlier}, "expired"},
		{"expires now", InterrogationChange{Desired: ChangeApproved, ExpiresAt: now}, "expired"},
		{"unknown stored state", InterrogationChange{Desired: "proposed"}, "proposed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.State(now); got != tc.state {
				t.Errorf("State() = %q, want %q", got, tc.state)
			}
		})
	}
}
