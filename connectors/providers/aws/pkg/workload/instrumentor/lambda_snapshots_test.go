package instrumentor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

type memorySnapshots map[string][]byte

func (s memorySnapshots) SaveLambdaSnapshot(_ context.Context, arn, id string, payload []byte) error {
	key := arn + id
	if _, ok := s[key]; !ok {
		s[key] = append([]byte(nil), payload...)
	}
	return nil
}
func (s memorySnapshots) LoadLambdaSnapshot(_ context.Context, arn, id string) ([]byte, error) {
	payload, ok := s[arn+id]
	if !ok {
		return nil, errors.New("snapshot missing")
	}
	return payload, nil
}

func TestLambdaSnapshotReferenceRestartRetryAndMissingState(t *testing.T) {
	ctx := context.Background()
	db := memorySnapshots{}
	i := &LambdaInstrumentor{snapshots: db}
	original := strings.Repeat("private-original-config", 100)
	m := buildManifest(map[string]string{EnvCollectorConfigURI: original}, "sdk-v1")
	marker, err := i.persistManifest(ctx, "function-a", m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(marker, "private-original") || len(marker) > 200 {
		t.Fatal("snapshot leaked into function marker")
	}
	ref, ok := parseManifest(marker)
	if !ok || ref.SnapshotID == "" {
		t.Fatal("reference missing")
	}
	// Another attempted AWS update fails: it must leave the accepted snapshot untouched.
	m.Layer = "sdk-v2"
	if _, err := i.persistManifest(ctx, "function-a", m); err != nil {
		t.Fatal(err)
	}
	restarted := &LambdaInstrumentor{snapshots: db}
	restored, err := restarted.loadManifest(ctx, "function-a", ref)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Layer != "sdk-v1" || *restored.PrevEnv[EnvCollectorConfigURI] != original {
		t.Fatal("retry overwrote accepted rollback state")
	}
	// JSONB reorders keys and spaces; canonical validation must still work.
	var document map[string]any
	_ = json.Unmarshal(db["function-a"+ref.SnapshotID], &document)
	db["function-a"+ref.SnapshotID], _ = json.MarshalIndent(document, "", "  ")
	if _, err := restarted.loadManifest(ctx, "function-a", ref); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.loadManifest(ctx, "function-b", ref); err == nil {
		t.Fatal("cross-function snapshot accepted")
	}
	delete(db, "function-a"+ref.SnapshotID)
	if _, err := restarted.loadManifest(ctx, "function-a", ref); err == nil {
		t.Fatal("missing state must stop rollback")
	}
}

func TestLambdaLegacySnapshotMigrationAndSignalChanges(t *testing.T) {
	ctx := context.Background()
	i := &LambdaInstrumentor{snapshots: memorySnapshots{}}
	original := "yaml:customer-collector"
	env := map[string]string{EnvCollectorConfigURI: original}
	m := buildManifest(env, "old-sdk")
	target := domaindest.OTLPTarget{Traces: true, Metrics: true, Logs: true}
	reconcileCustomerEdits(&m, env, desiredLambdaSharedEnv(i, false, "nodejs22.x", "", "service", target, false, "our-collector-v1"))
	applyHandlerEnv(env, "nodejs22.x", "", "service", target, false, "our-collector-v1")
	env[store.EnvOdigosManaged], _ = marshalManifest(m) // existing inline format
	m = buildManifest(env, "adot-sdk")
	reconcileCustomerEdits(&m, env, desiredLambdaSharedEnv(i, false, "nodejs22.x", "nodejs-lambda-adot", "service", target, false, "our-collector-v2"))
	// The last connector-generated config must never replace the original customer's config.
	if *m.PrevEnv[EnvCollectorConfigURI] != original {
		t.Fatal("destination change corrupted rollback")
	}
	marker, err := i.persistManifest(ctx, "function", m)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := parseManifest(marker)
	loaded, err := i.loadManifest(ctx, "function", ref)
	if err != nil {
		t.Fatal(err)
	}
	if *loaded.PrevEnv[EnvCollectorConfigURI] != original {
		t.Fatal("legacy migration lost original")
	}
}

func TestADOTRealisticExistingEnvironmentBudget(t *testing.T) {
	for _, lang := range []string{"java", "nodejs", "python"} {
		t.Run(lang, func(t *testing.T) {
			runtime := map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12"}[lang]
			original := strings.Repeat("original-collector-", 40) // reproduce the reported pre-existing ~728-byte config
			env := map[string]string{EnvCollectorConfigURI: original, "JAVA_LAMBDA_URL": strings.Repeat("x", 68), "ODIMALL_BASE_URL": strings.Repeat("x", 75), "PRODUCT_ID": "1", "HTTP_TIMEOUT_MS": "10000"}
			baseline := 0
			for k, v := range env {
				baseline += len(k) + len(v)
			}
			target := domaindest.OTLPTarget{HTTPEndpoint: "https://example.com:4318", Traces: true, Metrics: true, Logs: true}
			cfg, err := renderCollectorConfig(target, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := buildManifest(env, "arn:aws:lambda:us-east-2:025950698545:layer:odigos-otel-adot-"+lang+"-arm64:1")
			m.CollectorLayer = "arn:aws:lambda:us-east-2:025950698545:layer:odigos-otel-adot-collector-arm64:1"
			distro := lang + "-lambda-adot-application-signals"
			reconcileCustomerEdits(&m, env, desiredLambdaSharedEnv(&LambdaInstrumentor{}, false, runtime, distro, "odimall-lambda-"+lang, target, false, cfg))
			applyHandlerEnv(env, runtime, distro, "odimall-lambda-"+lang, target, false, cfg)
			if err := compactLambdaEnvironment(env, m, false, runtime, distro, "odimall-lambda-"+lang, target, nil, false); err != nil {
				t.Fatal(err)
			}
			i := &LambdaInstrumentor{snapshots: memorySnapshots{}}
			marker, err := i.persistManifest(context.Background(), "function", m)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateLambdaEnvSize(env); err != nil {
				t.Fatal(err)
			}
			size := 0
			for k, v := range env {
				size += len(k) + len(v)
			}
			t.Logf("%s configured environment: %d bytes; instrumentation adds %d bytes", lang, size, size-baseline)
			for key := range recipeFor(lang, distro).BootstrapEnvironment {
				if _, ok := env[key]; ok {
					t.Fatalf("bootstrap-owned %s consumes function environment", key)
				}
			}
			ref, _ := parseManifest(marker)
			restored, err := i.loadManifest(context.Background(), "function", ref)
			if err != nil {
				t.Fatal(err)
			}
			if *restored.PrevEnv[EnvCollectorConfigURI] != original {
				t.Fatal("original collector not restorable")
			}
		})
	}
}

func TestLambdaEnvironmentPreflightUTF8Boundary(t *testing.T) {
	if err := validateLambdaEnvSize(map[string]string{"KEY": strings.Repeat("x", 4093)}); err != nil {
		t.Fatal(err)
	}
	if err := validateLambdaEnvSize(map[string]string{"KEY": strings.Repeat("é", 2047)}); err == nil {
		t.Fatal("UTF-8 bytes were not counted")
	}
}

func TestAllLambdaPlansUseCompactEnvironment(t *testing.T) {
	for _, recipe := range lambdaRecipes() {
		p, err := DesiredLambdaPlanForDistro(recipe.Language, ActivationHandler, recipe.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range p.Fragments {
			for _, entry := range fragment.Env {
				if entry.Name != EnvLambdaConfig && entry.Name != store.EnvOdigosIaC && entry.Name != store.EnvExecWrapper {
					t.Fatalf("%s publishes a redundant variable %s", recipe.Name, entry.Name)
				}
			}
		}
	}
}

func TestEveryLambdaDistroCompactEnvironmentAndCustomerIsolation(t *testing.T) {
	for _, recipe := range lambdaRecipes() {
		for mask := 1; mask < 8; mask++ {
			if recipe.ApplicationSignals && mask&1 == 0 {
				continue
			}
			t.Run(recipe.Name+string(rune('0'+mask)), func(t *testing.T) {
				runtime := map[string]string{"java": "java21", "nodejs": "nodejs22.x", "python": "python3.12", "ruby": "ruby3.3"}[recipe.Language]
				original := map[string]string{EnvServiceName: "customer-service", EnvCollectorConfigURI: strings.Repeat("customer-config", 60), "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://customer.example/v1/traces", "OTEL_SDK_DISABLED": "true", "NODE_OPTIONS": "--max-old-space-size=2048"}
				env := cloneLambdaEnv(original)
				m := buildManifest(env, "sdk")
				target := domaindest.OTLPTarget{HTTPEndpoint: "https://destination.example", Traces: mask&1 != 0, Metrics: mask&2 != 0, Logs: mask&4 != 0}
				headers := map[string]string{"api-key": "test-credential"}
				cfg, err := renderCollectorConfig(target, headers)
				if err != nil {
					t.Fatal(err)
				}
				applyHandlerEnv(env, runtime, recipe.Name, "service", target, false, cfg)
				if err := compactLambdaEnvironment(env, m, false, runtime, recipe.Name, "service", target, headers, false); err != nil {
					t.Fatal(err)
				}
				for key, value := range original {
					if env[key] != value {
						t.Fatalf("modified customer variable %s", key)
					}
				}
				if len(env) != len(original)+2 {
					t.Fatalf("unexpected added variables: %v", env)
				}
				c, err := parseLambdaConfig(env[EnvLambdaConfig])
				if err != nil {
					t.Fatal(err)
				}
				if c.Signals != mask || c.Service != "service" || c.ApplicationSignals != recipe.ApplicationSignals || !c.HTTP {
					t.Fatal("wrong compact settings")
				}
				if c.FunctionLogs != (target.Logs && lambdaFunctionLogs(recipe.Language, recipe.ExecWrapper)) {
					t.Fatal("function log receiver selection does not match the selected distro")
				}
				if strings.Contains(env[EnvLambdaConfig], "pipelines") || len(env[EnvLambdaConfig]) > 250 {
					t.Fatal("collector pipeline duplicated in AWS environment")
				}
				env[store.EnvOdigosManaged], _ = marshalManifest(m)
				fn := store.LambdaFunction{Runtime: runtime, FunctionName: "service", Architecture: "x86_64"}
				virtual, err := expandedLambdaEnvironment(env, runtime, recipe.Name)
				if err != nil {
					t.Fatal(err)
				}
				if !lambdaSignalConfigurationMatches(virtual, target, recipe.Language, recipe.Name) {
					t.Fatal("compact config did not preserve SDK signal policy")
				}
				if diff := instrumentationDiff(false, fn, env, nil, "", target, false, "service", cfg, recipe.Name); len(diff) != 0 && !(len(diff) == 1 && diff[0] == componentInstrumentationLayer) {
					t.Fatal(diff)
				}
			})
		}
	}
}

func TestCompactRollbackPreservesCustomerEdits(t *testing.T) {
	original := map[string]string{EnvServiceName: "original", EnvCollectorConfigURI: "original-collector"}
	m := buildManifest(original, "sdk")
	m.AppliedFingerprint = "compact-protocol"
	env := cloneLambdaEnv(original)
	env[store.EnvExecWrapper] = "/opt/otel-handler"
	env[EnvLambdaConfig] = `{"v":1,"s":"ours","t":7,"e":"collector:4317"}`
	env[EnvServiceName] = "customer-changed-service"
	env[EnvCollectorConfigURI] = "customer-changed-collector"
	env["OTEL_NODE_ENABLED_INSTRUMENTATIONS"] = "customer-changed-instrumentations"
	restoreLambdaEnvironment(env, m)
	if env[EnvServiceName] != "customer-changed-service" || env[EnvCollectorConfigURI] != "customer-changed-collector" || env["OTEL_NODE_ENABLED_INSTRUMENTATIONS"] != "customer-changed-instrumentations" {
		t.Fatal("rollback overwrote customer edits")
	}
	if _, ok := env[EnvLambdaConfig]; ok {
		t.Fatal("compact configuration stranded after rollback")
	}
	if _, ok := env[store.EnvExecWrapper]; ok {
		t.Fatal("wrapper stranded after rollback")
	}
}

func TestLegacyCollectorMigrationPreservesOriginalSnapshot(t *testing.T) {
	original := "customer-collector-config"
	m := buildManifest(map[string]string{EnvCollectorConfigURI: original}, "sdk")
	target := domaindest.OTLPTarget{Endpoint: "previous:4317", Traces: true}
	old, _ := renderCollectorConfig(target, nil)
	// Released inline markers tracked exporters, but did not track the applied config hash.
	m.SignalEnv = map[string]string{EnvTracesExporter: "otlp", EnvLogsExporter: "none", EnvMetricsExporter: "none"}
	target.Endpoint = "next:4317"
	next, _ := renderCollectorConfig(target, nil)
	reconcileCustomerEdits(&m, map[string]string{EnvCollectorConfigURI: old}, map[string]string{EnvCollectorConfigURI: next})
	if m.PrevEnv[EnvCollectorConfigURI] == nil || *m.PrevEnv[EnvCollectorConfigURI] != original {
		t.Fatal("legacy migration replaced customer's original collector with our stale config")
	}
}
