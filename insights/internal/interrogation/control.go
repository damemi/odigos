package interrogation

import (
	"fmt"
	"time"
)

// Control is what the supervisor attaches to every runner message, under the
// run's state key "ctl": the control session's stop and extension as they
// are now, and why the message was sent. The graph applies whatever differs
// from its state, so delivering the same control twice changes nothing.
type Control struct {
	// Stop is the control session's stop, nil when none was recorded.
	Stop *Stop `json:"stop"`
	// ExtendedSeconds is the control session's total extension.
	ExtendedSeconds int `json:"extended_seconds"`
	// Resume says why a plain resume was sent, nil otherwise.
	Resume *Resume `json:"resume"`
	// Fail ends the run as interrupted with this error, when set.
	Fail string `json:"fail"`
}

// Resume is the failed invocation a plain resume follows.
type Resume struct {
	Error string `json:"error"`
	// ModelCall is whether the error came from the model call.
	ModelCall bool `json:"model_call"`
	// RetryAfter is how many seconds the supervisor waited.
	RetryAfter int `json:"retry_after"`
}

// applyControl applies to st what ctl says and st does not have yet, and
// reports whether the run must conclude. It writes nothing outside st: the
// extend handler moved the probes' expiry when it recorded the extension.
func applyControl(st *State, ctl Control, now time.Time) (evs []Event, conclude bool) {
	if ctl.Fail != "" {
		st.end(EndInterrupted, ctl.Fail)
		return nil, true
	}
	if ctl.Stop != nil {
		switch {
		case st.Stop == nil:
			stop := *ctl.Stop
			st.Stop = &stop
			st.end(EndStoppedByUser, "")
			evs = append(evs, stopRequested(st, stop, now))
		case ctl.Stop.Changes == ChangesRevert && st.Stop.Changes != ChangesRevert:
			// A later revert overrides an earlier keep, never the reverse.
			st.Stop.Changes = ChangesRevert
		}
	}
	if st.Stop != nil {
		return evs, true
	}

	if !st.StartedAt.IsZero() {
		limit := st.Request.Budget.TimeLimitSeconds + ctl.ExtendedSeconds
		deadline := st.StartedAt.Add(time.Duration(limit) * time.Second)
		switch {
		case st.DeadlineAt.IsZero():
			// The run's first deadline is not an extension.
			st.DeadlineAt = deadline
		case !deadline.Equal(st.DeadlineAt):
			seconds := int(deadline.Sub(st.DeadlineAt) / time.Second)
			st.DeadlineAt = deadline
			evs = append(evs, extended(st, seconds, now))
		}
	}

	if r := ctl.Resume; r != nil && r.Error != "" {
		st.StepFailures++
		st.LastError = r.Error
		node := st.Node
		st.nodeFailed(now)
		evs = append(evs, errorRaised(st, node, r.Error, r.RetryAfter, now))
		if r.ModelCall {
			evs = append(evs, modelCalled(st, usage{Model: st.Model}, OutcomeError, r.Error, now))
		}
		if st.StepFailures >= maxStepFailures {
			st.end(EndError, fmt.Sprintf("%s failed %d times in a row: %s", st.Step, st.StepFailures, r.Error))
			return evs, true
		}
	}
	return evs, false
}
