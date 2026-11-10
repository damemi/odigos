package instrumentor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/instrumentationplan"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
	"github.com/odigos-io/odigos/connectors/spec"
)

type instructionStore struct{ fn store.LambdaFunction }

func (s instructionStore) GetLambdaFunction(context.Context, string) (*store.LambdaFunction, bool, error) {
	return &s.fn, true, nil
}
func (instructionStore) OTLPExportTarget(context.Context) (domaindest.OTLPTarget, bool, error) {
	return domaindest.OTLPTarget{HTTPEndpoint: "https://telemetry.example.com", Traces: true, Metrics: true, Logs: true}, true, nil
}
func (instructionStore) OTLPExportHeaders(context.Context) (map[string]string, error) {
	return map[string]string{"test-header": "test-value"}, nil
}

type instructionLayers struct{}

func (instructionLayers) Resolve(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("instructions must not publish layers")
}
func (instructionLayers) ResolveDistro(context.Context, string, string, string, string) (string, error) {
	return "", fmt.Errorf("instructions must not publish layers")
}
func (instructionLayers) DiscoverExisting(_ context.Context, region, runtime, arch string) (string, error) {
	return instructionLayerARN(region, LanguageFromRuntime(runtime), arch), nil
}
func (instructionLayers) DiscoverDistro(_ context.Context, region, runtime, arch, distro string) (string, error) {
	return instructionLayerARN(region, lambdaArtifact(runtime, distro), arch), nil
}
func (instructionLayers) ResolveCollector(_ context.Context, region, _, arch string, readOnly bool) (string, error) {
	if !readOnly {
		return "", fmt.Errorf("instructions must not publish layers")
	}
	return instructionLayerARN(region, "adot-collector", arch), nil
}
func instructionLayerARN(region, family, arch string) string {
	return "arn:aws:lambda:" + region + ":123456789012:layer:odigos-otel-" + family + "-" + arch + ":1"
}

// Keep discovery on the previously installed upstream SDK while intent changes. The drawer must
// show the desired distro immediately, without waiting for the AWS update/discovery loop to finish.
func TestLambdaInstructionsFollowSelectedDistro(t *testing.T) {
	for _, recipe := range lambdaRecipes() {
		t.Run(recipe.Name, func(t *testing.T) {
			runtime := map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12", "ruby": "ruby3.4"}[recipe.Language]
			fn := store.LambdaFunction{FunctionName: "checkout", Region: "us-east-2", Runtime: runtime, Architecture: "arm64", Layers: []string{instructionLayerARN("us-east-2", recipe.Language, "arm64")}}
			p := &LambdaPlanner{store: instructionStore{fn}, layers: instructionLayers{}}
			planned, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{ServiceName: "checkout", Overrides: baseapi.SourceOverrides{OtelDistroName: recipe.Name}})
			if err != nil {
				t.Fatal(err)
			}
			values := planned.Resolved
			if values["<LAMBDA_OTEL_DISTRO>"] != recipe.Name || values["<LAMBDA_EXEC_WRAPPER>"] != recipe.ExecWrapper {
				t.Fatal("instructions do not select desired distro")
			}
			var config lambdaBootstrapConfig
			if err = json.Unmarshal([]byte(values["<ODIGOS_LAMBDA_CONFIG>"]), &config); err != nil {
				t.Fatal(err)
			}
			if config.Signals != 7 || !config.HTTP || config.ApplicationSignals != recipe.ApplicationSignals || config.FunctionLogs != lambdaFunctionLogs(recipe.Language, recipe.ExecWrapper) {
				t.Fatalf("wrong selected config: %+v", config)
			}
			var destination map[string]any
			if err = json.Unmarshal([]byte(values["<LAMBDA_CDK_DESTINATION>"]), &destination); err != nil {
				t.Fatal(err)
			}
			if destination["httpEndpoint"] != "https://telemetry.example.com" || destination["metrics"] != true || destination["logs"] != true || destination["headers"].(map[string]any)["test-header"] != "test-value" {
				t.Fatalf("CDK does not match destination: %v", destination)
			}
			arts := []baseapi.InstrumentationArtifact{}
			for _, a := range spec.MustGet("aws").InstrumentationArtifactsFor("aws.lambda", recipe.Language) {
				arts = append(arts, baseapi.InstrumentationArtifact{ID: a.ID, Variant: a.Variant, Title: a.Title, Description: a.Description, Template: a.Template, Prerequisites: a.Prerequisites})
			}
			rendered := instrumentationplan.ToModel(baseapi.InstrumentationPlanResult{Plan: planned.Plan, Resolved: values, Artifacts: arts}, time.Time{})
			if len(rendered.Documents) != 4 {
				t.Fatalf("expected all four delivery variants, got %d", len(rendered.Documents))
			}
			for _, doc := range rendered.Documents {
				if !strings.Contains(doc.Description, recipe.DisplayName) || !strings.Contains(doc.Description, recipe.Version) {
					t.Fatalf("%s: description does not identify selected SDK: %s", doc.Variant, doc.Description)
				}
				if !strings.Contains(doc.Content, recipe.ExecWrapper) {
					t.Fatalf("%s: selected entry point missing", doc.Variant)
				}
				if !strings.Contains(doc.Content, values[PlaceholderLayerARN]) {
					t.Fatalf("%s: selected layer missing", doc.Variant)
				}
				if strings.Contains(doc.Content, "odigos:collector-layer") != (recipe.Artifact != "") && doc.Variant != "cdk" {
					t.Fatalf("%s: collector attachment/tag does not match distro", doc.Variant)
				}
				if strings.Contains(doc.Content, "<LAMBDA_") {
					t.Fatalf("%s: unresolved SDK settings", doc.Variant)
				}
			}
		})
	}
}

func TestLambdaInstructionsWebAdapterAndLegacySelection(t *testing.T) {
	fn := store.LambdaFunction{FunctionName: "checkout", Region: "us-east-2", Runtime: "java21", Architecture: "arm64", Layers: []string{"arn:aws:lambda:us-east-2:123456789012:layer:LambdaAdapterLayerArm64:25"}}
	p := &LambdaPlanner{store: instructionStore{fn}, layers: instructionLayers{}}
	planned, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{ServiceName: "checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Resolved["<LAMBDA_EXEC_WRAPPER>"] != lambdaWebServerWrapper || !strings.Contains(planned.Resolved["<LAMBDA_CDK_MODE>"], "WEB_SERVER") {
		t.Fatal("Web Adapter instructions select handler activation")
	}
	var config lambdaBootstrapConfig
	if err = json.Unmarshal([]byte(planned.Resolved["<ODIGOS_LAMBDA_CONFIG>"]), &config); err != nil {
		t.Fatal(err)
	}
	if config.WebWrapper == nil || config.FunctionLogs {
		t.Fatal("incorrect Web Adapter config")
	}
	fn.Layers = nil
	p.store = instructionStore{fn}
	planned, err = p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{ServiceName: "checkout", Overrides: baseapi.SourceOverrides{OtelDistroName: "otel-proxy-handler"}})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Resolved["<LAMBDA_OTEL_DISTRO>"] != "java-lambda-otel-proxy-wrapper" || planned.Resolved["<LAMBDA_EXEC_WRAPPER>"] != "/opt/otel-proxy-handler" {
		t.Fatal("legacy override was not canonicalized")
	}
}
