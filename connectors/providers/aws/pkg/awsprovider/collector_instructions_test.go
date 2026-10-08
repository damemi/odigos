package awsprovider

import (
	"strings"
	"testing"

	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

func TestManualCollectorArtifactsContainDataPlaneAndCustomerOwnership(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 3}
	for _, document := range []string{
		awsCloudFormationDocument("eu-west-1", "vpc-123", "connector", "uid", "https://opamp.example", "registry.example/collector@sha256:abc", "512", "1024", "ARM64", 3, s).Content,
		awsTerraformDocument("eu-west-1", "vpc-123", "connector", "uid", "https://opamp.example", "registry.example/collector@sha256:abc", "512", "1024", "ARM64", 3, s).Content,
	} {
		for _, want := range []string{"odigos-iac", "odigos-connector-id", "odigos-managed", "4317", "4318", "13133", "network"} {
			if want == "odigos-managed" {
				if strings.Contains(document, want) {
					t.Fatalf("customer artifact contains ownership tag %q", want)
				}
				continue
			}
			if !strings.Contains(document, want) {
				t.Fatalf("artifact missing %q", want)
			}
		}
		if !strings.Contains(document, "3") || !strings.Contains(document, "ARM64") {
			t.Fatalf("artifact did not render desired count or architecture: %s", document)
		}
	}
}

func TestManualCollectorArtifactsRenderAutoscaling(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 7, TargetCPUPercent: 60, TargetMemoryPercent: 75}
	for _, document := range []string{
		awsCloudFormationDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content,
		awsTerraformDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content,
	} {
		for _, want := range []string{"autoscaling", "cpu", "memory", "60", "75"} {
			if !strings.Contains(strings.ToLower(document), want) {
				t.Fatalf("autoscaling artifact missing %q", want)
			}
		}
	}
}

func TestManualCollectorArtifactsAttachTargetsAndDeclareOnlyRealPlaceholders(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	for _, doc := range []struct {
		name      string
		content   string
		attach    string
		preserve  string
		crossZone string
	}{
		{"cloudformation", awsCloudFormationDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content, "LoadBalancers:", "preserve_client_ip.enabled", "load_balancing.cross_zone.enabled"},
		{"terraform", awsTerraformDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content, "load_balancer {", "preserve_client_ip = true", "load_balancing_cross_zone_enabled = \"true\""},
	} {
		if !strings.Contains(doc.content, doc.attach) || !strings.Contains(doc.content, "4317") || !strings.Contains(doc.content, "4318") ||
			!strings.Contains(doc.content, doc.preserve) || !strings.Contains(doc.content, doc.crossZone) {
			t.Errorf("%s does not match the automatic deployment's OTLP target groups", doc.name)
		}
		for _, unresolved := range artifactInputs(doc.content) {
			if !strings.Contains(doc.content, unresolved.Placeholder) {
				t.Errorf("%s claims absent placeholder %s", doc.name, unresolved.Placeholder)
			}
		}
	}
}
