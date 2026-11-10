package awsprovider

import (
	"context"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/source"
)

func TestFargateDistroValidationUsesRuntimeAliasesAndContainerDetection(t *testing.T) {
	p := &Provider{}
	w := &api.DiscoveredWorkload{Kind: "aws.fargate-task", RuntimeLanguage: "java", Details: &api.WorkloadDetails{Containers: []api.WorkloadContainer{{Name: "api", RuntimeLanguage: "javascript"}}}}
	for _, tc := range []struct {
		name, language, distro string
		invalid                bool
	}{
		{"node alias", "javascript", "nodejs-fargate-otel", false},
		{"dotnet alias", "csharp", "dotnet-community", false},
		{"wrong SDK", "java", "python-community", true},
		{"eBPF", "java", "java-ebpf-instrumented", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := source.Source{Kind: w.Kind, Config: source.SourceConfig{RuntimeOverride: source.RuntimeOverride{Language: tc.language}, DistroOverride: source.DistroOverride{OtelDistroName: tc.distro}}}
			err := p.ValidateSourceInstrumentation(context.Background(), src, w)
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v invalid=%v", err, tc.invalid)
			}
		})
	}
	src := source.Source{Kind: w.Kind, Config: source.SourceConfig{ContainerOverrides: []source.ContainerOverride{{ContainerName: "api", DistroOverride: source.DistroOverride{OtelDistroName: "nodejs-fargate-otel"}}}}}
	if err := p.ValidateSourceInstrumentation(context.Background(), src, w); err != nil {
		t.Fatal(err)
	}
	src.Config.ContainerOverrides[0].DistroOverride.OtelDistroName = "java-community"
	if err := p.ValidateSourceInstrumentation(context.Background(), src, w); err == nil {
		t.Fatal("incompatible per-container SDK accepted")
	}
}
