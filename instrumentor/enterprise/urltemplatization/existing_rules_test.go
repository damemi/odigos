package urltemplatization

import (
	"testing"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonapi "github.com/odigos-io/odigos/common/api"
	actionsapi "github.com/odigos-io/odigos/common/api/actions"
	"github.com/odigos-io/odigos/common/api/agentsignalconfig"
)

func TestCollectTemplatesFromIC_PrefersCollectorConfig(t *testing.T) {
	ic := &odigosv1.InstrumentationConfig{
		Spec: odigosv1.InstrumentationConfigSpec{
			WorkloadCollectorConfig: []commonapi.ContainerCollectorConfig{
				{
					ContainerName: "app",
					SourceCollectorConfig: commonapi.SourceCollectorConfig{
						UrlTemplatization: &actionsapi.UrlTemplatizationConfig{
							Templates: []string{"/users/{id}"},
						},
					},
				},
			},
			Containers: []odigosv1.ContainerAgentConfig{
				{
					ContainerName: "app",
					Traces: &agentsignalconfig.AgentTracesConfig{
						UrlTemplatization: &actionsapi.UrlTemplatizationConfig{
							Templates: []string{"/legacy/{id}"},
						},
					},
				},
				{
					ContainerName: "sidecar",
					Traces: &agentsignalconfig.AgentTracesConfig{
						UrlTemplatization: &actionsapi.UrlTemplatizationConfig{
							Templates: []string{"/health"},
						},
					},
				},
			},
		},
	}

	templates := collectTemplatesFromIC(ic)
	seen := map[string]bool{}
	for _, template := range templates {
		seen[template] = true
	}
	if !seen["/users/{id}"] {
		t.Fatal("expected collector template")
	}
	if !seen["/health"] {
		t.Fatal("expected sidecar agent template")
	}
	if seen["/legacy/{id}"] {
		t.Fatal("collector config should win over agent traces for same container")
	}
}
