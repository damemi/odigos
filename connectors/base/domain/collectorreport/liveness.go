// Package collectorreport turns a managed Collector's recorded state into the report a customer reads:
// one entry per Collector, with a status to branch on and a sentence to read.
//
// Pure — no I/O, and the clock is an argument. Every input is a value the store already holds or the
// reconcile pass already computed, so the same state always renders the same report.
package collectorreport

import (
	"fmt"
	"time"
)

// StaleAfterIntervals is how many heartbeat intervals of OpAMP silence make a Collector's configuration
// plane stale.
//
// Three, not one: a single missed interval is a slow API call or a pod restarting, and reporting that as
// a fault would make the report noise. Three is long enough to mean something and short enough that a
// dead configuration plane is not reported as healthy for an hour.
const StaleAfterIntervals = 3

// DefaultHeartbeatInterval matches config.KeyHeartbeatInterval's default. Used when a caller passes no
// interval, because the alternative — a zero window — makes every Collector stale the instant it
// connects.
const DefaultHeartbeatInterval = 30 * time.Second

// Liveness is what the OpAMP session says about a Collector, and it is deliberately three states rather
// than a boolean: "never connected" and "went quiet" have different causes and different remedies, and
// collapsing them loses the difference between a Collector nobody deployed and one that broke.
type Liveness string

const (
	// LivenessNeverConnected: no OpAMP connection has ever been seen. In manual mode this is the normal
	// starting state and is what AwaitingCustomer reports; in automatic mode it means Odigos is still
	// building it.
	LivenessNeverConnected Liveness = "never-connected"
	// LivenessLive: seen within the staleness window. The configuration plane is working.
	LivenessLive Liveness = "live"
	// LivenessStale: it connected once and has gone quiet. The Collector is very likely still running
	// and still exporting — the data path does not depend on OpAMP — but Odigos can no longer
	// change its configuration, which is a real loss and has to be said.
	LivenessStale Liveness = "stale"
)

// StaleWindow is how long silence is tolerated for a given heartbeat interval.
func StaleWindow(heartbeatInterval time.Duration) time.Duration {
	if heartbeatInterval <= 0 {
		heartbeatInterval = DefaultHeartbeatInterval
	}
	return heartbeatInterval * StaleAfterIntervals
}

// LivenessOf classifies a Collector's OpAMP session.
//
// lastSeen nil means never. A lastSeen in the FUTURE counts as live: clock skew between the connector
// and the database is real, and reading it as stale would report a working Collector as broken.
func LivenessOf(lastSeen *time.Time, heartbeatInterval time.Duration, now time.Time) Liveness {
	if lastSeen == nil || lastSeen.IsZero() {
		return LivenessNeverConnected
	}
	if now.Sub(*lastSeen) > StaleWindow(heartbeatInterval) {
		return LivenessStale
	}
	return LivenessLive
}

// Describe renders the liveness for the message, naming the window so a reader knows what "stale" cost.
func (l Liveness) Describe(lastSeen *time.Time, heartbeatInterval time.Duration, now time.Time) string {
	switch l {
	case LivenessLive:
		return "OpAMP connected"
	case LivenessStale:
		return fmt.Sprintf("OpAMP silent for %s (over the %s staleness window); Odigos cannot change "+
			"this Collector's configuration until it reconnects",
			since(lastSeen, now), StaleWindow(heartbeatInterval))
	default:
		return "never connected over OpAMP"
	}
}

// since renders a coarse relative age. Deterministic given now, and rounded because a report that said
// "4m13.219s ago" would differ on every read of unchanged state.
func since(at *time.Time, now time.Time) string {
	if at == nil || at.IsZero() {
		return "never"
	}
	d := now.Sub(*at)
	switch {
	case d < 0:
		// Clock skew between the connector and the database. Reported as just-now rather than as a
		// negative age, which reads as a bug in Odigos rather than a Collector that is fine.
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
