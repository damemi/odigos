// Package testconnection runs read-only reachability and permission checks for a prospective cloud
// connector, using credentials held in memory only. It is provider-agnostic: each provider registers a
// Tester (see aws.go / azure.go) and callers dispatch through Run. It is imported by central-backend so the
// onboarding / create-connector flow can validate a connector before any Secret or connector pod is
// created — therefore it must never persist credentials, log them, or echo them back. Each provider's
// probe calls mirror what that connector's discovery loop performs.
package testconnection

// CheckName identifies an individual connection check. Concrete names are declared by each provider.
type CheckName string

// CheckResult is the outcome of one check.
type CheckResult struct {
	Name        CheckName `json:"name"`
	DisplayName string    `json:"displayName"`
	Passed      bool      `json:"passed"`
	// Message is a human-readable result detail. It must never contain secret material.
	Message string `json:"message"`
	// Remediation suggests how to fix a failed check (e.g. which permissions to grant).
	Remediation string `json:"remediation,omitempty"`
}

// Result aggregates all checks.
type Result struct {
	// Success is true only if every check passed.
	Success bool `json:"success"`
	// AccountID / Identity are populated from the provider's identity probe when auth succeeds.
	AccountID string        `json:"accountId,omitempty"`
	Identity  string        `json:"identity,omitempty"`
	Checks    []CheckResult `json:"checks"`
}

func (r *Result) add(c CheckResult) { r.Checks = append(r.Checks, c) }

// finalize sets Success based on the collected checks.
func (r *Result) finalize() {
	r.Success = len(r.Checks) > 0
	for _, c := range r.Checks {
		if !c.Passed {
			r.Success = false
		}
	}
}
