package instrumentor

import (
	"context"
	"errors"
	"slices"
	"testing"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

func TestEveryLambdaDistroHasACollectorlessTwin(t *testing.T) {
	recipes := lambdaRecipes()
	for _, recipe := range recipes {
		if recipe.NoCollector {
			continue
		}
		i := slices.IndexFunc(recipes, func(r lambdaRecipe) bool { return r.Name == recipe.Name+"-no-collector" })
		if i < 0 {
			t.Fatalf("%s has no collector-less variant", recipe.Name)
		}
		twin := recipes[i]
		if !twin.NoCollector || twin.IsDefault || twin.Language != recipe.Language || twin.ExecWrapper != recipe.ExecWrapper || twin.Artifact != recipe.Artifact || twin.ApplicationSignals != recipe.ApplicationSignals {
			t.Fatalf("%s does not mirror %s", twin.Name, recipe.Name)
		}
	}
}

func TestCollectorlessDistrosAreHandlerModeOnly(t *testing.T) {
	for _, language := range []string{"java", "nodejs", "python", "ruby"} {
		for _, d := range LambdaDistros(language, ActivationWebServer) {
			if recipeFor(language, d.Name).NoCollector {
				t.Fatalf("%s offered for web-server activation", d.Name)
			}
		}
	}
}

func TestCollectorlessDistroRequiresAnOTLPHTTPDestination(t *testing.T) {
	grpcOnly := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true}
	if err := validateLambdaNoCollectorTarget("nodejs", "nodejs-lambda-otel-no-collector", grpcOnly); !errors.Is(err, ErrUnsupportedInstrumentation) {
		t.Fatalf("gRPC-only destination accepted: %v", err)
	}
	if err := validateLambdaNoCollectorTarget("nodejs", "nodejs-lambda-otel", grpcOnly); err != nil {
		t.Fatalf("collector distro rejected: %v", err)
	}
	both := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, HTTPEndpoint: "https://otlp.example", Traces: true}
	if err := validateLambdaNoCollectorTarget("nodejs", "nodejs-lambda-otel-no-collector", both); err != nil {
		t.Fatal(err)
	}
	if got := lambdaExportTarget("nodejs", "nodejs-lambda-otel-no-collector", both); got.Endpoint != "" || got.Insecure || got.HTTPEndpoint != both.HTTPEndpoint {
		t.Fatalf("collector-less target kept gRPC: %+v", got)
	}
	if got := lambdaExportTarget("nodejs", "nodejs-lambda-otel", both); got != both {
		t.Fatalf("collector target changed: %+v", got)
	}
}

// A destination with both endpoints renders a gRPC collector configuration for collector distros. A
// collector-less function must still converge, rather than drift on every reconcile.
func TestCollectorlessFunctionConvergesAgainstADualEndpointDestination(t *testing.T) {
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, HTTPEndpoint: "https://otlp.example", Traces: true, Metrics: true, Logs: true}
	headers := map[string]string{"api-key": "secret"}
	for _, recipe := range lambdaRecipes() {
		if !recipe.NoCollector {
			continue
		}
		t.Run(recipe.Name, func(t *testing.T) {
			runtime := map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12", "ruby": "ruby3.3"}[recipe.Language]
			target := lambdaExportTarget(recipe.Language, recipe.Name, dest)
			cfg, err := renderCollectorConfig(target, headers)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			m := buildManifest(env, "sdk")
			applyHandlerEnv(env, runtime, recipe.Name, "service", target, false, cfg)
			if err := compactLambdaEnvironment(env, m, false, runtime, recipe.Name, "service", target, headers, false); err != nil {
				t.Fatal(err)
			}
			c, err := parseLambdaConfig(env[EnvLambdaConfig])
			if err != nil {
				t.Fatal(err)
			}
			if !c.NoCollector || !c.HTTP || c.Endpoint != dest.HTTPEndpoint || c.FunctionLogs {
				t.Fatalf("wrong collector-less configuration: %+v", c)
			}
			env[store.EnvOdigosManaged], _ = marshalManifest(m)
			fn := store.LambdaFunction{Runtime: runtime, FunctionName: "service", Architecture: "x86_64"}
			if diff := instrumentationDiff(false, fn, env, nil, "", target, false, "service", cfg, recipe.Name); len(diff) != 0 {
				t.Fatalf("converged function drifts: %v", diff)
			}
			collector := "arn:aws:lambda:us-east-1:0:layer:" + publishedLayerName("adot-collector", "x86_64") + ":1"
			if diff := instrumentationDiff(false, fn, env, []string{collector}, "", target, false, "service", cfg, recipe.Name); !slices.Contains(diff, componentInstrumentationLayer) {
				t.Fatalf("leftover collector layer not reported: %v", diff)
			}
		})
	}
}

func TestCollectorlessADOTPlanAttachesNoCollectorLayer(t *testing.T) {
	for _, recipe := range lambdaRecipes() {
		if recipe.Artifact == "" {
			continue
		}
		p, err := DesiredLambdaPlanForDistro(recipe.Language, ActivationHandler, recipe.Name)
		if err != nil {
			t.Fatal(err)
		}
		layers := 0
		for _, fragment := range p.Fragments {
			if fragment.Kind == plan.KindLayer {
				layers++
			}
		}
		if want := map[bool]int{true: 1, false: 2}[recipe.NoCollector]; layers != want {
			t.Fatalf("%s plans %d layers, want %d", recipe.Name, layers, want)
		}
		if recipe.NoCollector && !slices.Contains(p.Notes, lambdaNoCollectorNote) {
			t.Fatalf("%s plan does not explain its drawbacks", recipe.Name)
		}
		fn := store.LambdaFunction{Runtime: map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12"}[recipe.Language], Architecture: "arm64"}
		arn, err := resolveLambdaCollector(context.Background(), nil, fn, recipe.Name, true)
		if recipe.NoCollector && (arn != "" || err != nil) {
			t.Fatalf("%s resolved a collector layer: %q, %v", recipe.Name, arn, err)
		}
	}
}

func TestCollectorlessFunctionOutputLogsAreReportedAsNotCollected(t *testing.T) {
	routing := domaindest.Routing{Found: true, Target: domaindest.OTLPTarget{HTTPEndpoint: "https://otlp.example", Traces: true, Logs: true}}
	note := lambdaSignalRoutingNote(routing, "java21", "java-lambda-otel-wrapper-no-collector")
	if note == nil || note.Message != "Collecting traces" {
		t.Fatalf("note = %+v", note)
	}
	if note := lambdaSignalRoutingNote(routing, "java21", "java-lambda-otel-wrapper"); note != nil && note.Code == "collected-signals" {
		t.Fatalf("collector distro reported as dropping logs: %+v", note)
	}
	if note := lambdaSignalRoutingNote(routing, "nodejs22.x", "nodejs-lambda-otel-no-collector"); note != nil && note.Code == "collected-signals" {
		t.Fatalf("SDK logs reported as dropped: %+v", note)
	}
}

// The Ruby layer ships gems for Ruby 3.3 and 3.4 only; on any other Ruby the function cannot initialize.
func TestRubyIsLimitedToTheABIsItsLayerShips(t *testing.T) {
	for _, distro := range []string{"", "ruby-lambda-otel-no-collector"} {
		for _, runtime := range []string{"ruby3.3", "ruby3.4"} {
			if err := ValidateLambdaDistroRuntime(runtime, ActivationHandler, distro); err != nil {
				t.Fatalf("%s rejected for %s: %v", runtime, distro, err)
			}
		}
		for _, runtime := range []string{"ruby3.2", "ruby4.0"} {
			if err := ValidateLambdaDistroRuntime(runtime, ActivationHandler, distro); err == nil {
				t.Fatalf("%s accepted for %q although the layer has no gems for it", runtime, distro)
			}
		}
	}
}
