// Package collectorstatus is the shared report vocabulary for managed-Collector decisions.
//
// Ingress, transport, credential lifecycle, and siblings all write the same
// managed_collectors.status / message columns — one Severity here keeps them aligned.
// Tiny and dependency-free on purpose.
package collectorstatus

// Severity is how loudly a condition is reported.
// Degraded and Error map to Collector status; Info and Pending are message-only.
type Severity string

const (
	// SeverityInfo: worth saying, not a fault (e.g. plaintext default).
	SeverityInfo Severity = "Info"
	// SeverityPending: not usable yet; nothing for the customer to fix — clears on its own.
	SeverityPending Severity = "Pending"
	// SeverityDegraded: working, in a posture the customer must be told about.
	SeverityDegraded Severity = "Degraded"
	// SeverityError: refused — nothing written; undeployed things are not created.
	SeverityError Severity = "Error"
)

// Problem is one reportable condition.
// Message should name the config key or action. Callers branch on Code/Severity, never Message.
type Problem struct {
	Code     string
	Severity Severity
	Message  string
}

// HasError reports whether any problem is SeverityError.
func HasError(problems []Problem) bool {
	for _, p := range problems {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}
