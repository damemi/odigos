package clickhouse

import "time"

// decisionEpoch is the "unset timestamp" sentinel for a resolution column a row doesn't own; max-merge ignores it.
var decisionEpoch = time.Unix(0, 0)

// firstSeenFuture is the sentinel first_seen a decision row writes so min-merge keeps the real earliest occurrence.
var firstSeenFuture = time.Unix(1<<40, 0)

// stickyDecisionStatus reduces an aggregate finding's three max-merged decision timestamps to which sticky bucket wins: terminal (accept/allow) beats ties, else a dismiss that outdates any reopen, else open.
func stickyDecisionStatus(terminalAt, dismissedAt, reopenedAt time.Time) (terminal, dismissed bool) {
	switch {
	case terminalAt.After(decisionEpoch) && !dismissedAt.After(terminalAt) && !reopenedAt.After(terminalAt):
		return true, false
	case dismissedAt.After(decisionEpoch) && dismissedAt.After(reopenedAt):
		return false, true
	default:
		return false, false
	}
}
