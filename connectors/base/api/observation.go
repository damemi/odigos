package api

import "context"

// Conformance is how completely a workload's LIVE instrumentation matches the instrumentation Odigos
// wants for it. It exists because "is the ownership marker present" is not the same question as "is the
// instrumentation actually correct", and manual (IaC) mode makes the difference load-bearing: in automatic
// mode the connector converges any gap on the next reconcile, so a partial application is a moment in
// time; in manual mode nobody converges it, so a customer who applies the marker and nothing else stays
// half-instrumented forever — and would otherwise be reported as healthy.
//
// It is deliberately a VERDICT over normalized facts, not a state name: the provider observes and fills
// this in, and the base decides what a report should say about it (see workloadreport). That split is what
// keeps four providers from growing four different state machines.
type Conformance string

const (
	// ConformanceUnknown means no successful observation has been made yet, or the last one failed. It is
	// the zero value on purpose: a workload nobody has observed must not read as either conformant or
	// drifted. A read failure is NEVER drift.
	ConformanceUnknown Conformance = ""
	// ConformanceAbsent means no Odigos instrumentation is attributable to this connector at all — there is
	// nothing applied, as opposed to something applied incorrectly. A customer's own unrelated
	// OpenTelemetry setup lands here, which is why it must not be reported as an error.
	ConformanceAbsent Conformance = "Absent"
	// ConformancePartial means instrumentation is attributable to this connector but does not match what is
	// wanted: a component is missing, or carries a stale value (a renamed service, a changed destination, a
	// layer that was detached while the marker survived). MissingComponents names which.
	ConformancePartial Conformance = "Partial"
	// ConformanceConformant means every component of the desired instrumentation is present and current.
	ConformanceConformant Conformance = "Conformant"
)

// InstrumentationObserver observes ONE instrumentation type read-only in a single pass, and persists what
// it found through the provider's own inventory so ListWorkloads can report it. It is the manual-mode
// counterpart to Reconciler and is keyed by spec instrumentation type name the same way.
//
// Two hard rules:
//
//   - It MUST NOT mutate provider resources. It is the only instrumentation code path that runs for a
//     manual-mode type, so it is also the only thing standing between "Odigos observes your workload" and
//     "Odigos changed your workload".
//   - It classifies nothing. It records normalized facts — a Conformance verdict, the components that do
//     not match, whether a change is still settling — and never a report state, stage or issue code. The
//     base turns those facts into a report (base/domain/workloadreport), which is what keeps the state
//     machine in one place across providers.
//
// A failed read must be recorded as an observation error, never as drift: the base preserves the last
// confirmed verdict and raises a transient issue instead of telling a customer their deployment broke.
type InstrumentationObserver interface {
	Observe(ctx context.Context) error
}

// WorkloadObserverRegistry is an OPTIONAL capability a Provider may implement to supply read-only
// observers, asserted at the single call site that runs them exactly the way Prerequisite and
// StartupVerifier already are. It is not part of Provider: observation lands per provider (one Wave-4
// ticket each), and putting it on the interface would break every provider module in one commit for the
// sake of returning an empty map.
//
// The base runs an observer only for a type whose capability mode is manual — in automatic mode the
// reconciler already converges and reports the same ground truth, so observing would be a second read of
// the provider's control plane for an answer the connector is about to enforce anyway.
type WorkloadObserverRegistry interface {
	WorkloadObservers() map[string]InstrumentationObserver
}
