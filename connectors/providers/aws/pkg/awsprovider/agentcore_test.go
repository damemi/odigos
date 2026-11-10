package awsprovider

import (
	"context"
	"slices"
	"testing"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

func TestAgentCoreRegisteredAcrossModesWithoutManagedRouting(t *testing.T) {
	p := &Provider{}
	if !slices.Contains(p.SupportedWorkloadKinds(), agentcore.Kind) || p.WorkloadDetectors()[agentcore.Kind] == nil || p.Reconcilers()[agentcore.Kind] == nil || p.WorkloadObservers()[agentcore.Kind] == nil || p.WorkloadPlanners()[agentcore.Kind] == nil {
		t.Fatal("AgentCore is missing a discovery/automatic/manual/plan registration")
	}
	if !p.RequiresGlobalOTLPDestination(agentcore.Kind) || p.RequiresGlobalOTLPDestination("aws.lambda") {
		t.Fatal("global-only destination policy affects the wrong kinds")
	}
	// A nil provider store is intentional: this path must return before looking up any managed
	// route, collector readiness or health. AgentCore's planner resolves the global target itself.
	if _, handled, err := p.ResolveWorkloadRouting(context.Background(), baseapi.DiscoveredWorkload{Kind: agentcore.Kind}); handled || err != nil {
		t.Fatalf("AgentCore entered managed routing: %v %v", handled, err)
	}
	for _, language := range []string{"python", "nodejs"} {
		w := baseapi.DiscoveredWorkload{Kind: agentcore.Kind, RuntimeLanguage: language}
		p.DescribeInstrumentation(context.Background(), &w)
		if len(w.AvailableDistros) != 2 || w.EffectiveDistro != language+"-agentcore-adot" {
			t.Fatalf("wrong default/choices: %+v", w)
		}
	}
}
