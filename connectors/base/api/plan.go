package api

import (
	"context"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
)

// InstrumentationPlanner computes the instrumentation ONE workload needs, as anchored fragments a customer
// can apply in their own deployment definition. It is the read-only counterpart to Reconciler for the
// question "what would you do", asked of a connector that in manual mode will never do it.
//
// It must not mutate provider resources and should avoid provider calls it does not need: a plan is served on
// the GraphQL read path, in response to a client opening a workload. A planner that needs a live read to
// resolve something (a container's name, a layer version) should make it, but leave the value unresolved
// rather than fail the whole plan — the language-independent part is still the bulk of what a customer
// applies, and a plan that errors out because one value could not be looked up tells them nothing at all.
//
// The plan it returns must be the SAME desired state the connector's own injector would produce, built from
// the same recipes. That is the property the whole feature rests on: nothing converges a manual-mode
// workload, so a plan that differs from what the observer then judges against produces a customer who applied
// the instructions correctly and is told their deployment has drifted.
type InstrumentationPlanner interface {
	// PlanFor returns the plan for one discovered workload, or nil when this planner has nothing to say
	// about it (a language it cannot instrument, a workload shape it does not support). A nil result is not an
	// error: the report already explains an uninstrumentable workload, and inventing a plan for one would be
	// instructions that cannot work.
	PlanFor(ctx context.Context, w DiscoveredWorkload) (*PlannedInstrumentation, error)
}

// PlannedInstrumentation is one planner's answer: the desired state, plus the placeholder values only the
// PROVIDER can resolve.
//
// The two travel together because resolving a placeholder is not a separate concern from planning it — the
// same read that tells the planner a Lambda function runs behind the web adapter also tells it which layer
// version the function carries, and splitting them would mean reading twice or passing the read's result
// through a second interface.
type PlannedInstrumentation struct {
	Plan plan.Plan
	// Resolved maps a placeholder token to the value this workload gives it. Only the provider-owned ones
	// belong here — a container's name, a layer version ARN; the base fills the connector-owned ones (the
	// destination endpoint, the reported service name, the region) and OVERRIDES anything set for them here,
	// because those are properties of the connector rather than of the workload and must not differ per
	// provider.
	//
	// A token left out is one nothing could resolve. That is a normal answer, not a gap to paper over with an
	// empty string: a client renders it as a field for the customer to fill in, which is the truth for a
	// container name on a task definition holding several applications, or for a Lambda layer Odigos does not
	// publish yet.
	Resolved map[string]string
}

// WorkloadPlannerRegistry is an OPTIONAL capability a Provider may implement to supply planners, asserted at
// the single call site that uses them exactly the way WorkloadObserverRegistry, Prerequisite and
// StartupVerifier already are.
//
// It is not part of Provider for the same reason observation is not: it lands per provider, and putting it on
// the interface would break every provider module in one commit for the sake of returning an empty map. A
// provider that registers no planner simply serves no plan, and the query resolves to null — which is what a
// client already has to handle for a workload whose type has no instrumentation at all.
type WorkloadPlannerRegistry interface {
	// WorkloadPlanners maps a spec instrumentation type name to its planner.
	WorkloadPlanners() map[string]InstrumentationPlanner
}

// InstrumentationPlanResult is a plan as served: the provider's plan, the delivery artifacts declared for its
// type, and the placeholder values the base could resolve.
//
// The split is deliberate. The provider owns the desired state and knows which values only it can resolve (a
// container name, a layer ARN); the base owns the values that are properties of the CONNECTOR rather than of
// the workload — the destination endpoint, the reported service name, the region — and resolving those in
// four providers would be four chances to disagree about, say, which of the two endpoint forms goes where.
type InstrumentationPlanResult struct {
	// Workload identity, echoed so a client can match the plan to the report it came from.
	ServiceName string
	Kind        string
	Region      string
	// InstrumentationType is the spec type this plan is for.
	InstrumentationType string
	// Mode is who applies it: spec.ModeAutomatic or spec.ModeManual.
	Mode string
	// Plan is the desired state.
	Plan plan.Plan
	// Artifacts are the provider's spec-declared delivery variants for this type, in spec order.
	Artifacts []InstrumentationArtifact
	// Resolved maps a placeholder token to its value for this workload. A token absent from the map, or
	// mapped to "", is one nothing could resolve — the client renders it as a field to fill in.
	Resolved map[string]string
}

// InstrumentationArtifact is one delivery variant of a plan: a snippet plus the ordered steps that say where
// it goes. It mirrors spec.SetupArtifact, narrowed to the fields an instrumentation artifact uses — the
// permission-rendering knobs belong to the onboarding artifacts and would be meaningless here.
type InstrumentationArtifact struct {
	ID           string
	Variant      string
	VariantLabel string
	Title        string
	Description  string
	Steps         []string
	Format        string
	Template      string
	Prerequisites []string
}
