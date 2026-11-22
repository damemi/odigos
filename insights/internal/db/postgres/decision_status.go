package postgres

import "time"

// firstSeenFuture is the first_seen a decision row writes so the LEAST merge
// keeps the real earliest occurrence.
var firstSeenFuture = time.Unix(1<<40, 0).UTC()

// stickyDecisionStatus reduces a finding's three decision timestamps to which sticky bucket wins: terminal (accept/allow) beats ties, else a dismiss that outdates any reopen, else open.
func stickyDecisionStatus(terminalAt, dismissedAt, reopenedAt time.Time) (terminal, dismissed bool) {
	switch {
	case terminalAt.After(epoch) && !dismissedAt.After(terminalAt) && !reopenedAt.After(terminalAt):
		return true, false
	case dismissedAt.After(epoch) && dismissedAt.After(reopenedAt):
		return false, true
	default:
		return false, false
	}
}
