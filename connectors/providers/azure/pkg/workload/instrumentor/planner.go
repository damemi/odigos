package instrumentor

import (
	"context"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
)

// ContainerAppsPlanner serves the Container Apps instrumentation plan for one discovered app.
//
// It makes NO Azure calls: it runs on the GraphQL read path, and the resolved language it needs is already in
// the connector's inventory from discovery.
type ContainerAppsPlanner struct {
	// image is the odigos-agents image the init container copies from, so a connector configured with an
	// override publishes instructions naming the image it would actually use.
	image string
}

// NewContainerAppsPlanner builds the planner. image is config.AgentImage.
func NewContainerAppsPlanner(image string) *ContainerAppsPlanner {
	return &ContainerAppsPlanner{image: image}
}

// PlanFor implements baseapi.InstrumentationPlanner.
func (p *ContainerAppsPlanner) PlanFor(_ context.Context, w baseapi.DiscoveredWorkload) (*baseapi.PlannedInstrumentation, error) {
	// The source runtime override wins over detection, matching detectLanguage's precedence: an override
	// exists because detection was wrong, so publishing the detected language's agent hands the customer the
	// one they already rejected.
	lang := normalizeLanguage(w.Overrides.RuntimeLanguage)
	if lang == "" {
		lang = normalizeLanguage(w.RuntimeLanguage)
	}
	if lang == "" {
		// No plan rather than a partial one: the report already says the runtime is undetermined or
		// unsupported, and a language-independent plan would tell a customer to stage an agent directory that
		// does not exist.
		return nil, nil
	}

	pl, err := DesiredPlan(lang, p.image)
	if err != nil {
		return nil, err
	}
	// No container resolution: the Container Apps connector keeps no per-container inventory (which is also
	// why its workload details report a null containers list), so <YOUR_CONTAINER> stays unresolved and a
	// client renders it as a field to fill in. That is the truth rather than a guess at which of an app's
	// containers is the application.
	return &baseapi.PlannedInstrumentation{Plan: pl}, nil
}
