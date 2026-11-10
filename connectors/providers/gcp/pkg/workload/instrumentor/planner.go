package instrumentor

import (
	"context"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
)

// CloudRunPlanner serves the Cloud Run instrumentation plan for one discovered service.
//
// It resolves the plan from the workload's own facts and makes NO Google Cloud calls: it runs on the GraphQL
// read path, in response to a client opening a workload, and the report itself is served without touching the
// control plane. Everything it needs — the resolved language, the container inventory — discovery already
// wrote to the connector's inventory.
type CloudRunPlanner struct {
	// image is the odigos-agents image the staging sidecar copies from, so a connector configured with an
	// override publishes instructions naming the image it would actually use.
	image string
}

// NewCloudRunPlanner builds the planner. image is config.AgentImage.
func NewCloudRunPlanner(image string) *CloudRunPlanner { return &CloudRunPlanner{image: image} }

// PlanFor implements baseapi.InstrumentationPlanner.
func (p *CloudRunPlanner) PlanFor(_ context.Context, w baseapi.DiscoveredWorkload) (*baseapi.PlannedInstrumentation, error) {
	// The source runtime override wins over detection, matching detectLanguage's own precedence — a customer
	// who set an override did so because detection was wrong, and publishing instructions for the detected
	// language would hand them the agent they already rejected.
	lang := normalizeLanguage(w.Overrides.RuntimeLanguage)
	if lang == "" {
		lang = normalizeLanguage(w.RuntimeLanguage)
	}
	if lang == "" || !SupportedLanguage(lang) {
		// No plan rather than a partial one. The report already says why this service cannot be instrumented
		// (undetermined or unsupported runtime, with the remedy), and a language-independent plan would tell a
		// customer to stage an agent directory that does not exist.
		return nil, nil
	}

	pl, err := DesiredPlan(lang, p.image)
	if err != nil {
		return nil, err
	}
	return &baseapi.PlannedInstrumentation{Plan: pl, Resolved: resolveCloudRunPlaceholders(w)}, nil
}

// resolveCloudRunPlaceholders fills what this workload determines. Only the container name is provider-owned
// here — the endpoint, the reported service name and the region are the connector's and the base fills them.
func resolveCloudRunPlaceholders(w baseapi.DiscoveredWorkload) map[string]string {
	out := map[string]string{}
	if name := soleInstrumentableContainer(w); name != "" {
		out[plan.PlaceholderContainer] = name
	}
	return out
}

// soleInstrumentableContainer returns the container name when the workload has exactly one Odigos could
// instrument, and "" otherwise.
//
// Exactly one, deliberately. Resolving the placeholder for a multi-container service would pick one of several
// applications for the customer, and the plan's container-scoped fragments would then instrument that one and
// silently leave the rest — which reads as success. Leaving it unresolved makes the choice visible, which is
// the correct outcome when there IS a choice.
func soleInstrumentableContainer(w baseapi.DiscoveredWorkload) string {
	if w.Details == nil {
		return ""
	}
	var found string
	for _, c := range w.Details.Containers {
		if !c.Eligible {
			continue
		}
		if found != "" {
			return ""
		}
		found = c.Name
	}
	return found
}
