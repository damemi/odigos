package instrumentor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

func TestADOTSignalSelectionAndSwitch(t *testing.T) {
	for _, tc := range []struct{ language, runtime string }{{"java", "java21"}, {"python", "python3.12"}, {"nodejs", "nodejs22.x"}} {
		for _, app := range []bool{false, true} {
			distro := tc.language + "-lambda-adot"
			if app {
				distro += "-application-signals"
			}
			for mask := 1; mask < 8; mask++ {
				t.Run(fmt.Sprintf("%s/%d", distro, mask), func(t *testing.T) {
					target := domaindest.OTLPTarget{Endpoint: "https://example.com", Traces: mask&1 != 0, Metrics: mask&2 != 0, Logs: mask&4 != 0}
					env := map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://wrong.example.com/v1/traces"}
					manifest := buildManifest(env, "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-"+tc.language+"-x86_64:1")
					desired := desiredLambdaSharedEnv(&LambdaInstrumentor{}, false, tc.runtime, distro, "service", target, false, "collector")
					reconcileCustomerEdits(&manifest, env, desired)
					applyHandlerEnv(env, tc.runtime, distro, "service", target, false, "collector")
					if !lambdaSignalConfigurationMatches(env, target, tc.language, distro) {
						t.Fatalf("selected distro does not match injected environment: %v", env)
					}
					for key, value := range target.SignalExporters() {
						if env[key] != value {
							t.Errorf("%s = %s, want %s", key, env[key], value)
						}
					}
					if env["OTEL_AWS_APPLICATION_SIGNALS_ENABLED"] != fmt.Sprint(app) {
						t.Fatal("Application Signals preset ignored")
					}
					if _, exists := env["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"]; exists {
						t.Fatal("bootstrap-owned endpoint persisted in Lambda configuration")
					}
					// Switching back must remove AWS preset settings and preserve the original customer endpoint.
					env[store.EnvOdigosManaged], _ = marshalManifest(manifest)
					applyHandlerEnv(env, tc.runtime, "", "service", target, false, "collector")
					if !lambdaSignalConfigurationMatches(env, target, tc.language, "") {
						t.Fatal("switch back did not clear ADOT configuration")
					}
					if manifest.PrevEnv["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] == nil || *manifest.PrevEnv["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] != "https://wrong.example.com/v1/traces" {
						t.Fatal("lost rollback endpoint")
					}
				})
			}
		}
	}
}

func TestADOTCatalogAndPlans(t *testing.T) {
	for _, lang := range []string{"java", "nodejs", "python"} {
		for _, suffix := range []string{"", "-application-signals"} {
			name := lang + "-lambda-adot" + suffix
			if _, err := ResolveLambdaDistro(lang, ActivationWebServer, name); err == nil {
				t.Fatal("unverified web adapter accepted")
			}
			p, err := DesiredLambdaPlanForDistro(lang, ActivationHandler, name)
			if err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			for _, f := range p.Fragments {
				for _, e := range f.Env {
					env[e.Name] = e.Value
				}
			}
			if env[EnvLambdaConfig] != "<ODIGOS_LAMBDA_CONFIG>" {
				t.Fatal("plan omitted AWS preset")
			}
			if !strings.HasPrefix(lambdaArtifact(lang, name), "adot-") {
				t.Fatal("selected upstream layer for ADOT")
			}
		}
	}
	if ValidateLambdaDistroRuntime("java8.al2", ActivationHandler, "java-lambda-adot") == nil {
		t.Fatal("unsupported Java runtime accepted")
	}
	if ValidateLambdaDistroRuntime("python3.9", ActivationHandler, "python-lambda-adot") == nil {
		t.Fatal("unsupported Python runtime accepted")
	}
}

func TestADOTSwitchReplacesPreviousSDKLayer(t *testing.T) {
	before := []string{"arn:aws:lambda:us-east-1:123456789012:layer:customer:1", "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-java-x86_64:1"}
	desired := "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-adot-java-x86_64:2"
	after := normalizeInstrumentationLayers(before, desired)
	if len(after) != 2 || after[0] != before[0] || after[1] != desired {
		t.Fatalf("SDKs stacked or customer layer removed: %v", after)
	}
}

func TestADOTCollectorConformanceAndGC(t *testing.T) {
	sdk := "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-adot-python-x86_64:1"
	collector := "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-adot-collector-x86_64:1"
	fn := store.LambdaFunction{FunctionARN: "py", FunctionName: "service", Region: "us-east-1", Runtime: "python3.12", Architecture: "x86_64", Instrumented: true, Layers: []string{sdk, collector}}
	dest := domaindest.OTLPTarget{Endpoint: "https://example.com", Traces: true}
	env := map[string]string{}
	applyHandlerEnv(env, fn.Runtime, "python-lambda-adot", "service", dest, false, "collector")
	manifest := buildManifest(map[string]string{}, sdk)
	manifest.CollectorLayer = collector
	env[store.EnvOdigosManaged], _ = marshalManifest(manifest)
	if diff := instrumentationDiff(false, fn, env, fn.Layers, sdk, dest, false, "service", "collector", "python-lambda-adot"); len(diff) != 0 {
		t.Fatal(diff)
	}
	if diff := instrumentationDiff(false, fn, env, []string{sdk}, sdk, dest, false, "service", "collector", "python-lambda-adot"); len(diff) == 0 {
		t.Fatal("missing collector layer reported conformant")
	}
	if desiredPrint(false, fn, sdk, false, "service", "collector", "python-lambda-adot") == desiredPrint(false, fn, sdk, false, "service", "collector", "python-lambda-adot-application-signals") {
		t.Fatal("Application Signals switch did not invalidate observation cache")
	}
	eligible := eligibleLayerKeys(map[string]enabledLambdaSource{}, []store.LambdaFunction{fn})
	if eligible["us-east-1/adot-collector/x86_64"] || eligible["us-east-1/adot-python/x86_64"] {
		t.Fatal("active ADOT layers marked for garbage collection")
	}
	fn.Instrumented = false
	fn.Layers = nil
	eligible = eligibleLayerKeys(map[string]enabledLambdaSource{}, []store.LambdaFunction{fn})
	if !eligible["us-east-1/adot-collector/x86_64"] || !eligible["us-east-1/adot-python/x86_64"] {
		t.Fatal("unused ADOT layers cannot be garbage collected")
	}
}

func TestADOTEnvironmentFitsLambdaLimit(t *testing.T) {
	for _, language := range []string{"java", "nodejs", "python"} {
		runtime := map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12"}[language]
		distro := language + "-lambda-adot-application-signals"
		dest := domaindest.OTLPTarget{Endpoint: "https://otlp.example.com:4318", Traces: true, Metrics: true, Logs: true}
		cfg, err := renderCollectorConfig(dest, nil)
		if err != nil {
			t.Fatal(err)
		}
		env := map[string]string{}
		m := buildManifest(env, "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-adot-"+language+"-x86_64:1")
		m.CollectorLayer = "arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-adot-collector-x86_64:1"
		reconcileCustomerEdits(&m, env, desiredLambdaSharedEnv(&LambdaInstrumentor{}, false, runtime, distro, "lambda-service", dest, false, cfg))
		applyHandlerEnv(env, runtime, distro, "lambda-service", dest, false, cfg)
		env[store.EnvOdigosManaged], _ = marshalManifest(m)
		size := 0
		for k, v := range env {
			size += len(k) + len(v)
		}
		if size > 4096 {
			t.Errorf("%s: managed environment needs %d bytes (Lambda maximum 4096)", language, size)
		} else {
			t.Logf("%s: %d bytes", language, size)
		}
	}
}

func TestADOTJavaBridgeRecognizedForRollback(t *testing.T) {
	wrapper := execWrapperForRuntime("java21", "java-lambda-adot")
	if wrapper != "/opt/odigos-adot-java-handler" || !isOdigosExecWrapper(wrapper) {
		t.Fatal("Java ADOT bridge cannot be attributed/rolled back")
	}
}

func TestADOTSwitchPreservesCustomerInstrumentationList(t *testing.T) {
	env := map[string]string{"OTEL_NODE_ENABLED_INSTRUMENTATIONS": "http,pino"}
	dest := domaindest.OTLPTarget{Traces: true, Logs: true}
	manifest := buildManifest(env, "sdk")
	i := &LambdaInstrumentor{}
	reconcileCustomerEdits(&manifest, env, desiredLambdaSharedEnv(i, false, "nodejs22.x", "nodejs-lambda-adot", "service", dest, false, "collector"))
	applyHandlerEnv(env, "nodejs22.x", "nodejs-lambda-adot", "service", dest, false, "collector")
	if _, exists := env["OTEL_NODE_ENABLED_INSTRUMENTATIONS"]; exists {
		t.Fatal("long managed list duplicated in the Lambda environment")
	}
	reconcileCustomerEdits(&manifest, env, desiredLambdaSharedEnv(i, false, "nodejs22.x", "", "service", dest, false, "collector"))
	if previous := manifest.PrevEnv["OTEL_NODE_ENABLED_INSTRUMENTATIONS"]; previous == nil || *previous != "http,pino" {
		t.Fatal("SDK switch replaced the customer's original instrumentation list")
	}
}

func TestADOTCLISnippetsMatchInjectedEnvironment(t *testing.T) {
	for _, language := range []string{"java", "nodejs", "python"} {
		runtime := map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12"}[language]
		for _, suffix := range []string{"", "-application-signals"} {
			name := language + "-lambda-adot" + suffix
			values := lambdaSnippetReplacements(language, name)
			for k, v := range lambdaLayerSnippetReplacements(language, name, "sdk-layer", "collector-layer") {
				values[k] = v
			}
			for _, artifact := range spec.MustGet("aws").InstrumentationArtifactsFor("aws.lambda", language) {
				if artifact.Variant != "aws-cli" {
					continue
				}
				body := plan.Substitute(artifact.Template, values)
				start := strings.Index(body, "{\n  \"Variables\"")
				if start < 0 {
					t.Fatal("CLI environment document missing")
				}
				end := strings.Index(body[start:], "\nJSON")
				if end < 0 {
					t.Fatal("CLI environment document unterminated")
				}
				var document struct{ Variables map[string]string }
				if err := json.Unmarshal([]byte(body[start:start+end]), &document); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				document.Variables[store.EnvOdigosIaC] = strings.ReplaceAll(document.Variables[store.EnvOdigosIaC], "<OTLP_INSECURE>", "false")
				var compact map[string]any
				if err := json.Unmarshal([]byte(strings.ReplaceAll(lambdaConfigFromEnvironment(document.Variables), "<OTLP_INSECURE>", "false")), &compact); err != nil {
					t.Fatal(err)
				}
				if (compact["applicationSignals"] == true) != (suffix != "") {
					t.Fatal("wrong AWS preset in compact configuration")
				}
				if document.Variables[store.EnvOdigosIaC] != "" || document.Variables[store.EnvOdigosManaged] != "" {
					t.Fatal("marker leaked into Lambda environment")
				}
				if !strings.Contains(body, `"odigos:sdk-layer":"sdk-layer"`) || !strings.Contains(body, `"odigos:collector-layer":"collector-layer"`) {
					t.Fatal("CLI does not tag both attached layers")
				}
				if !strings.Contains(body, `--layers $EXISTING "sdk-layer" "collector-layer"`) {
					t.Fatal("CLI does not attach both layers")
				}
				if document.Variables[store.EnvExecWrapper] != "<WRAPPER>" {
					t.Fatal("unexpected CLI wrapper substitution")
				}
				if !strings.Contains(body, `WRAPPER="`+execWrapperForRuntime(runtime, name)+`"`) {
					t.Fatal("CLI startup script does not match selected SDK")
				}
			}
		}
	}
}
