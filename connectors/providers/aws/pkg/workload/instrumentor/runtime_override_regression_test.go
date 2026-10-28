package instrumentor

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

func TestWorkloadOverridesApplyBeforeContainerSelection(t *testing.T) {
	for _, tc := range []struct{ name, image, language, version string }{
		{"opaque-python", "registry.example.com/checkout:prod", "python", ""},
		{"missing-php-version", "php:latest", "php", "8.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspector := newECSInspector(nil, nil, "registry.odigos.io/odigos-fargate-agents:v0.0.19", nil, nil)
			td := taskDefOf(container("app", tc.image, true))
			targets, excluded, err := inspector.selectTargets(context.Background(), td, enabledFargateSource{language: tc.language, version: tc.version}, store.ECSService{}, domaindest.OTLPTarget{HTTPEndpoint: "https://otlp.example.com:4318", Traces: true})
			if err != nil || len(targets) != 1 {
				t.Fatalf("workload override failed: targets=%d excluded=%v err=%v", len(targets), excluded, err)
			}
		})
	}
}

func TestImageLoaderEnvironmentSurvivesReconcileAndRollback(t *testing.T) {
	inspector := newECSInspector(nil, nil, testAgentsImage, nil, fakeInspector{cfg: &ImageConfig{Env: []string{"JAVA_VERSION=21", "JAVA_TOOL_OPTIONS=-Xmx256m"}}})
	td := taskDefOf(container("app", "registry.example.com/app:prod", true))
	targets, _, err := inspector.selectTargets(context.Background(), td, enabledFargateSource{}, store.ECSService{}, domaindest.OTLPTarget{HTTPEndpoint: "https://otlp.example.com", Traces: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = applyInstrumentation(td, targets, nil, testAgentsImage, "app", "us-east-1", false); err != nil {
		t.Fatal(err)
	}
	env := containerEnvMap(&td.ContainerDefinitions[0])
	if !strings.HasPrefix(env[EnvJavaToolOptions], "-Xmx256m ") {
		t.Fatalf("lost image loader: %v", env)
	}
	if !alreadyInstrumented(td, targets, testAgentsImage, "app", "us-east-1", false) {
		t.Fatal("inherited loader never converges")
	}
	// A transient inspection failure must not discard the last known inherited loader.
	targets[0].runtime.imageEnv = nil
	if _, err = applyInstrumentation(td, targets, nil, testAgentsImage, "app", "us-east-1", false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(containerEnvMap(&td.ContainerDefinitions[0])[EnvJavaToolOptions], "-Xmx256m ") {
		t.Fatal("lost inherited loader during inspection outage")
	}
	input, owned := rollbackInstrumentation(td, nil)
	if !owned {
		t.Fatal("missing rollback")
	}
	if _, present := containerEnvMap(&input.ContainerDefinitions[0])[EnvJavaToolOptions]; present {
		t.Fatal("rollback must reveal the image ENV, not freeze it into the task definition")
	}
}

func TestEnvironmentFilesRequireInlineConfiguration(t *testing.T) {
	recipe, err := planRecipes.recipeFor(LangJava, "")
	if err != nil {
		t.Fatal(err)
	}
	app := container("app", "image", true)
	setContainerEnv(&app, EnvJavaToolOptions, "-Xmx256m")
	app.EnvironmentFiles = []ecstypes.EnvironmentFile{{Value: aws.String("arn:aws:s3:::config/env"), Type: ecstypes.EnvironmentFileTypeS3}}
	if err := instrumentContainer(&app, containerTarget{name: "app", recipe: recipe}, "app", "us-east-1", false); err == nil {
		t.Fatal("unknown loader from environment file was overwritten")
	}
}

func TestMixedLanguageManualPlanIncludesEveryContainer(t *testing.T) {
	p := NewECSFargatePlanner(testAgentsImage)
	got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{RuntimeLanguage: "multiple", Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{{Name: "api", Eligible: true, RuntimeLanguage: "java"}, {Name: "worker", Eligible: true, RuntimeLanguage: "python"}}}})
	if err != nil || got == nil {
		t.Fatalf("plan=%v error=%v", got, err)
	}
	prose := plan.Prose(got.Plan)
	for _, want := range []string{"api", "worker", "JAVA_TOOL_OPTIONS", "PYTHONPATH", "/instrumentations/java", "/instrumentations/python"} {
		if !strings.Contains(prose, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestImageTransportOverrideRequiresRemoval(t *testing.T) {
	recipe, err := planRecipes.recipeFor(LangJava, "")
	if err != nil {
		t.Fatal(err)
	}
	app := container("app", "image", true)
	target := containerTarget{name: "app", recipe: recipe, runtime: resolvedRuntime{imageEnv: map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://old.example.com"}}}
	if err := instrumentContainer(&app, target, "app", "us-east-1", false); err == nil {
		t.Fatal("image transport override can bypass the configured destination")
	}
}
