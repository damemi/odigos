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

// Status is the coarse state written to managed_collectors.status and shown for a Collector. Distinct
// from Severity: a Severity describes one condition, a Status describes the Collector.
type Status string

const (
	// StatusPending: nothing has been built yet.
	StatusPending Status = "Pending"
	// StatusDeploying: infrastructure is being created or converged.
	StatusDeploying Status = "Deploying"
	// StatusReady: running, reachable, and configured.
	StatusReady Status = "Ready"
	// StatusDegraded: working, in a posture worth telling the customer about.
	StatusDegraded Status = "Degraded"
	// StatusError: refused or broken.
	StatusError Status = "Error"
	// StatusAwaitingCustomer: manual mode, waiting on something only the customer can do — deploy the
	// published artifacts, write a rotated secret, redeploy after an endpoint move. Never derived from a
	// problem; the reconciler knows it is in manual mode and nothing else does.
	StatusAwaitingCustomer Status = "AwaitingCustomer"
)

// StatusFor reduces a set of problems to the status they imply, falling back to base when they imply
// none.
//
// Only Degraded and Error map, exactly as Severity documents: Info and Pending are message-only. That
// distinction is what keeps a Collector that is deploying normally, and reporting so, from being shown
// as faulty — and it is why base exists at all. The reconciler chooses base (Pending, Deploying, Ready
// or AwaitingCustomer) from what it is actually doing; problems can only make it worse, never better.
func StatusFor(problems []Problem, base Status) Status {
	worst := base
	for _, p := range problems {
		switch p.Severity {
		case SeverityError:
			// Terminal for this pass: nothing a later problem says can be worse.
			return StatusError
		case SeverityDegraded:
			worst = StatusDegraded
		}
	}
	return worst
}
