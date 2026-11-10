package instrumentor

import (
	"fmt"
	"reflect"
	"testing"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

func TestAllFargateRecipesSelectSignalsAndRestoreOverrides(t *testing.T) {
	for _, lang := range []string{LangJava, LangPython, LangNodeJS, LangDotNet, LangPHP, LangRuby} {
		for bits := 1; bits < 8; bits++ {
			t.Run(fmt.Sprintf("%s/%d", lang, bits), func(t *testing.T) {
				version := ""
				if lang == LangPHP {
					version = "8.3"
				}
				if lang == LangRuby {
					version = "3.3"
				}
				rec := testRecipe(t, lang, version)
				td := sampleTaskDef()
				for _, key := range domaindest.SignalTransportOverrides() {
					setContainerEnv(&td.ContainerDefinitions[0], key, "customer-value")
				}
				original := containerEnvMap(&td.ContainerDefinitions[0])
				target := ecsExportTarget{endpoint: "http://collector:4318", protocol: otlpProtocolHTTP, traces: bits&1 != 0, metrics: bits&2 != 0, logs: bits&4 != 0}
				targets := oneTarget(0, rec, target)
				in, err := applyInstrumentation(td, targets, nil, testAgentsImage, "service", "us-east-1", false)
				if err != nil {
					t.Fatal(err)
				}
				applied := tdFromRegisterInput(in)
				if !alreadyInstrumented(applied, targets, testAgentsImage, "service", "us-east-1", false) {
					t.Fatal("injected task is not converged")
				}
				if !target.signals().SignalConfigurationMatches(containerEnvMap(&applied.ContainerDefinitions[0]), lang) {
					t.Fatal("SDK signal policy mismatch")
				}
				rollbackContainer(&applied.ContainerDefinitions[0])
				if got := containerEnvMap(&applied.ContainerDefinitions[0]); !reflect.DeepEqual(got, original) {
					t.Fatalf("rollback changed customer environment: got %v want %v", got, original)
				}
			})
		}
	}
}

func TestAllLambdaHandlersSelectSignalsAndSnapshotOverrides(t *testing.T) {
	for _, runtime := range []string{"python3.12", "nodejs22.x", "ruby3.3", "java21"} {
		distros := []string{""}
		if runtime == "java21" {
			distros = []string{"java-lambda-otel-agent", "java-lambda-otel-wrapper", "java-lambda-otel-proxy-wrapper", "java-lambda-otel-sqs-wrapper", "java-lambda-otel-stream-wrapper"}
		}
		for _, distro := range distros {
			for bits := 1; bits < 8; bits++ {
				t.Run(fmt.Sprintf("%s/%s/%d", runtime, distro, bits), func(t *testing.T) {
					env := map[string]string{store.EnvExecWrapper: "customer-wrapper"}
					for _, key := range domaindest.SignalTransportOverrides() {
						env[key] = "customer-value"
					}
					manifest := buildManifest(env, "layer")
					dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: bits&1 != 0, Metrics: bits&2 != 0, Logs: bits&4 != 0}
					cfg := mustRenderCollectorConfig(t, dest)
					applyHandlerEnv(env, runtime, distro, "service", dest, false, cfg)
					if env[store.EnvExecWrapper] == "" {
						t.Fatal("handler distro did not resolve its wrapper")
					}
					if !dest.LambdaSignalConfigurationMatches(env, LanguageFromRuntime(runtime)) || !collectorExportAsDesired(env, dest, cfg) {
						t.Fatalf("incorrect signal wiring: %v", env)
					}
					for _, key := range domaindest.SignalTransportOverrides() {
						if manifest.PrevEnv[key] == nil || *manifest.PrevEnv[key] != "customer-value" {
							t.Fatalf("override %s not preserved for rollback", key)
						}
					}
				})
			}
		}
	}
}

func TestLambdaSignalTransitionsKeepOriginalRollbackValues(t *testing.T) {
	env := map[string]string{}
	manifest := buildManifest(env, "layer")
	inst := &LambdaInstrumentor{}
	for bits := 1; bits < 8; bits++ {
		dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: bits&1 != 0, Metrics: bits&2 != 0, Logs: bits&4 != 0}
		cfg := mustRenderCollectorConfig(t, dest)
		reconcileCustomerEdits(&manifest, env, inst.desiredSharedEnv(false, "python3.12", "service", dest, false, cfg))
		applyHandlerEnv(env, "python3.12", "", "service", dest, false, cfg)
		for _, key := range []string{EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter, "OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED"} {
			if manifest.PrevEnv[key] != nil {
				t.Fatalf("transition %d captured our previous %s as a customer value", bits, key)
			}
		}
	}
}
