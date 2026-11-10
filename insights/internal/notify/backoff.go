package notify

import "time"

// MaxAttempts is how many consecutive failures a (integration, finding) pair
// tolerates before the delivery is parked as dead and left for an operator
// retry. Reset by a manual retry or when the finding changes action.
const MaxAttempts = 10

// backoffSchedule is the wait before retry n (1-based); later retries use the
// last value. The first step equals the poll interval so a transient blip is
// retried on the very next tick.
var backoffSchedule = []time.Duration{
	15 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
}

// Backoff returns how long to wait after the n-th consecutive failure.
func Backoff(failures int) time.Duration {
	if failures < 1 {
		return backoffSchedule[0]
	}
	if failures > len(backoffSchedule) {
		return backoffSchedule[len(backoffSchedule)-1]
	}
	return backoffSchedule[failures-1]
}
