package instrumentor

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

type lambdaRouteResolverFunc func(context.Context, []store.LambdaFunction) (map[string]domaindest.Routing, error)

func (f lambdaRouteResolverFunc) ResolveLambdaRoutes(ctx context.Context, functions []store.LambdaFunction) (map[string]domaindest.Routing, error) {
	return f(ctx, functions)
}

func TestLambdaConsumersUseTheSameCompletePassRouteResolver(t *testing.T) {
	fn := store.LambdaFunction{FunctionARN: "arn:aws:lambda:us-east-1:1:function:checkout"}
	want := domaindest.Routing{
		Found:   true,
		Via:     domaindest.RouteCollector,
		Outcome: domaindest.RoutingCollector,
		Target:  domaindest.OTLPTarget{Endpoint: "regional:4317", Traces: true},
	}
	resolver := lambdaRouteResolverFunc(func(_ context.Context, got []store.LambdaFunction) (map[string]domaindest.Routing, error) {
		if len(got) != 1 || got[0].FunctionARN != fn.FunctionARN {
			t.Fatalf("resolver received %+v, want the enabled function", got)
		}
		return map[string]domaindest.Routing{fn.FunctionARN: want}, nil
	})

	inst := NewLambda(nil, nil, nil, nil, resolver)
	got, err := inst.resolveLambdaRoutes(context.Background(), []store.LambdaFunction{fn})
	if err != nil {
		t.Fatalf("instrumentor route resolution: %v", err)
	}
	if !reflect.DeepEqual(got[fn.FunctionARN], want) {
		t.Fatalf("instrumentor route = %+v, want %+v", got[fn.FunctionARN], want)
	}

	obs := NewLambdaObserver(nil, nil, nil, resolver)
	got, err = obs.resolveLambdaRoutes(context.Background(), []store.LambdaFunction{fn})
	if err != nil {
		t.Fatalf("observer route resolution: %v", err)
	}
	if !reflect.DeepEqual(got[fn.FunctionARN], want) {
		t.Fatalf("observer route = %+v, want %+v", got[fn.FunctionARN], want)
	}
}

func TestLambdaRoutingErrorIsMarkedAndRecognizedSeparately(t *testing.T) {
	message := lambdaRoutingErrorMessage("no global OTLP destination")
	if !isLambdaRoutingError(message) {
		t.Fatalf("routing error %q was not recognized", message)
	}
	if isLambdaRoutingError("instrumentation layer could not be resolved") {
		t.Fatal("ordinary instrumentation failures must not be classified as routing errors")
	}
	if !isLambdaRoutingFailureNote(&baseapi.WorkloadRoutingNote{Code: "routing-error"}) {
		t.Fatal("routing-error notes must be recognized for stale-error recovery")
	}
	if !isLambdaRoutingFailureNote(&baseapi.WorkloadRoutingNote{Code: "no-destination"}) {
		t.Fatal("no-destination notes must be recognized for stale-error recovery")
	}
	if isLambdaRoutingFailureNote(&baseapi.WorkloadRoutingNote{Code: "collector-not-ready"}) {
		t.Fatal("nonfatal readiness notes must not be treated as routing errors")
	}
}

func TestLambdaRoutingErrorNoteDescribesMissingGlobalFallback(t *testing.T) {
	routing := domaindest.Routing{
		Via:     domaindest.RouteDirect,
		Outcome: domaindest.RoutingError,
		Reason:  domaindest.ReasonNoDestination,
		Note:    "No applicable destination: no global OTLP destination is configured, so this workload cannot export telemetry.",
	}
	note := lambdaRoutingNote(routing)
	if note == nil || note.Code != string(domaindest.ReasonNoDestination) || note.Message != routing.Note {
		t.Fatalf("routing note = %+v, want the resolver's no-destination explanation", note)
	}
}

func TestLambdaCollectorConfigOwnsDownstreamHeaders(t *testing.T) {
	direct := domaindest.OTLPTarget{Endpoint: "global:4317", Insecure: true, Traces: true}
	collector := domaindest.OTLPTarget{Endpoint: "regional:4317", Insecure: true, Traces: true}
	headers := map[string]string{"authorization": "Bearer secret"}

	directCfg, err := renderCollectorConfig(direct, headers)
	if err != nil {
		t.Fatalf("render direct config: %v", err)
	}
	if !strings.Contains(directCfg, "authorization") || !strings.Contains(directCfg, "Bearer secret") {
		t.Fatalf("direct config lost destination headers: %s", directCfg)
	}
	collectorCfg, err := renderCollectorConfig(collector, nil)
	if err != nil {
		t.Fatalf("render Collector config: %v", err)
	}
	if strings.Contains(collectorCfg, "authorization") || strings.Contains(collectorCfg, "Bearer secret") {
		t.Fatalf("Collector config must not carry terminal destination headers: %s", collectorCfg)
	}
}

func TestLambdaConfigUpdateNeverAttachesVpc(t *testing.T) {
	input := lambdaConfigUpdateInput("arn:aws:lambda:us-east-1:1:function:checkout", []string{"layer"}, map[string]string{"key": "value"}, aws.String("revision"))
	if input.VpcConfig != nil {
		t.Fatal("Lambda instrumentation must never set VpcConfig")
	}
	if aws.ToString(input.FunctionName) == "" || input.Environment == nil || len(input.Layers) != 1 {
		t.Fatalf("update input lost instrumentation fields: %+v", input)
	}
}

// TestBuildManifestSnapshotsCustomerConfig is the core safety guarantee for issue 1: rollback must be
// able to restore a customer's pre-existing values and never delete config it did not add. The manifest
// snapshots the customer's prior values for shared keys, records absence for keys we introduce, and
// treats odigos-owned keys as ours (so rollback deletes them).
func TestBuildManifestSnapshotsCustomerConfig(t *testing.T) {
	layerARN := "arn:aws:lambda:us-east-1:000000000000:layer:odigos-otel-python-x86_64:3"
	// A function with a customer-set OTEL_SERVICE_NAME (and nothing of ours yet).
	env := map[string]string{
		EnvServiceName: "customer-service",
	}

	m := buildManifest(env, layerARN)

	if m.Layer != layerARN {
		t.Fatalf("Layer = %q, want %q", m.Layer, layerARN)
	}
	// Customer's OTEL_SERVICE_NAME must be captured for restore.
	if got := m.PrevEnv[EnvServiceName]; got == nil || *got != "customer-service" {
		t.Errorf("PrevEnv[%s] = %v, want pointer to %q", EnvServiceName, got, "customer-service")
	}
	// Keys we introduce were absent before — recorded as nil so rollback deletes them.
	for _, k := range []string{store.EnvExecWrapper, EnvCollectorEndpoint, EnvCollectorInsecure} {
		got, ok := m.PrevEnv[k]
		if !ok {
			t.Errorf("PrevEnv missing key %s", k)
			continue
		}
		if got != nil {
			t.Errorf("PrevEnv[%s] = %q, want nil (absent before)", k, *got)
		}
	}
}

// TestBuildManifestTreatsOdigosKeysAsOurs guards that on a pre-marker (legacy) function the snapshot does
// not mistake our own values for the customer's: the odigos-namespaced collector vars and our exec-wrapper
// sentinel are recorded as absent so rollback deletes them rather than "restoring" them.
func TestBuildManifestTreatsOdigosKeysAsOurs(t *testing.T) {
	// Either language exec wrapper is ours — including a Java function a pre-fix connector left with the
	// Python /opt/otel-instrument value, which must still be recorded as absent so rollback deletes it.
	for _, wrapper := range []string{execWrapperInstrument, execWrapperHandler} {
		env := map[string]string{
			store.EnvExecWrapper: wrapper,
			EnvCollectorEndpoint: "collector:4317",
			EnvCollectorInsecure: "true",
		}
		m := buildManifest(env, "")
		for _, k := range []string{store.EnvExecWrapper, EnvCollectorEndpoint, EnvCollectorInsecure} {
			if got := m.PrevEnv[k]; got != nil {
				t.Errorf("wrapper %s: PrevEnv[%s] = %q, want nil (ours, not customer's)", wrapper, k, *got)
			}
		}
	}
}

// TestBuildManifestPreservesCustomerExecWrapper guards that a customer's non-Odigos exec wrapper is
// captured for restore rather than deleted.
func TestBuildManifestPreservesCustomerExecWrapper(t *testing.T) {
	env := map[string]string{store.EnvExecWrapper: "/opt/custom-wrapper"}
	m := buildManifest(env, "")
	got := m.PrevEnv[store.EnvExecWrapper]
	if got == nil || *got != "/opt/custom-wrapper" {
		t.Fatalf("PrevEnv[%s] = %v, want pointer to %q", store.EnvExecWrapper, got, "/opt/custom-wrapper")
	}
}

// TestBuildManifestReusesExistingSnapshot guards that a re-instrument (e.g. a layer version bump) keeps
// the original customer snapshot and only refreshes the layer — never recording our own values as the
// customer's.
func TestBuildManifestReusesExistingSnapshot(t *testing.T) {
	orig := managedManifest{
		Version: managedManifestVersion,
		Layer:   "arn:aws:lambda:us-east-1:0:layer:odigos-otel-python-x86_64:2",
		PrevEnv: map[string]*string{EnvServiceName: ptr("customer-service")},
	}
	marker, err := marshalManifest(orig)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		store.EnvOdigosManaged: marker,
		EnvServiceName:         "now-our-value", // our current value, must NOT overwrite the snapshot
	}
	newLayer := "arn:aws:lambda:us-east-1:0:layer:odigos-otel-python-x86_64:4"

	m := buildManifest(env, newLayer)

	if m.Layer != newLayer {
		t.Errorf("Layer = %q, want refreshed %q", m.Layer, newLayer)
	}
	if got := m.PrevEnv[EnvServiceName]; got == nil || *got != "customer-service" {
		t.Errorf("PrevEnv[%s] = %v, want original snapshot %q", EnvServiceName, got, "customer-service")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	in := managedManifest{
		Version: managedManifestVersion,
		Layer:   "arn:aws:lambda:us-east-1:0:layer:odigos-otel-python-x86_64:1",
		PrevEnv: map[string]*string{EnvServiceName: ptr("svc"), EnvCollectorEndpoint: nil},
	}
	s, err := marshalManifest(in)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := parseManifest(s)
	if !ok {
		t.Fatal("parseManifest returned ok=false for a valid marker")
	}
	if out.Layer != in.Layer {
		t.Errorf("Layer = %q, want %q", out.Layer, in.Layer)
	}
	if got := out.PrevEnv[EnvServiceName]; got == nil || *got != "svc" {
		t.Errorf("PrevEnv[%s] = %v, want %q", EnvServiceName, got, "svc")
	}
	if got, ok := out.PrevEnv[EnvCollectorEndpoint]; !ok || got != nil {
		t.Errorf("PrevEnv[%s] = (%v, ok=%v), want (nil, true)", EnvCollectorEndpoint, got, ok)
	}
}

func TestParseManifestRejectsGarbage(t *testing.T) {
	if _, ok := parseManifest(""); ok {
		t.Error("empty marker should not parse")
	}
	if _, ok := parseManifest("not-json"); ok {
		t.Error("non-JSON marker should not parse")
	}
}

func ptr(s string) *string { return &s }

// GC is per language+arch: a region's java layer is reclaimed when its last java Lambda is uninstrumented,
// even while python is still instrumented in the same region.
func TestEligibleLayerKeysPerLanguage(t *testing.T) {
	functions := []store.LambdaFunction{
		// python in us-east-1: instrumented -> layer in use.
		{FunctionARN: "py1", Region: "us-east-1", Runtime: "python3.12", Architecture: "x86_64", Instrumented: true},
		// java in us-east-1: not instrumented, no enabled source -> orphaned, eligible for GC.
		{FunctionARN: "jv1", Region: "us-east-1", Runtime: "java17", Architecture: "x86_64", Instrumented: false},
		// nodejs in eu-west-1: enabled (about to instrument) -> in use, not eligible.
		{FunctionARN: "nd1", Region: "eu-west-1", Runtime: "nodejs20.x", Architecture: "arm64", Instrumented: false},
		// unsupported runtime -> no layer key, never eligible.
		{FunctionARN: "go1", Region: "us-east-1", Runtime: "go1.x", Architecture: "x86_64"},
	}
	enabled := map[string]enabledLambdaSource{"nd1": {}}

	got := eligibleLayerKeys(enabled, functions)

	if !got["us-east-1/java/x86_64"] {
		t.Errorf("java layer should be eligible for GC, got %v", got)
	}
	if got["us-east-1/python/x86_64"] {
		t.Error("python layer is in use (instrumented), must not be GC-eligible")
	}
	if got["eu-west-1/nodejs/arm64"] {
		t.Error("nodejs layer has an enabled source, must not be GC-eligible")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly one eligible key, got %v", got)
	}
}

// instrumentedLayerKeys reports the in-use keys used to decide whether a layer is an orphan (refreshable).
func TestInstrumentedLayerKeys(t *testing.T) {
	functions := []store.LambdaFunction{
		{Region: "us-east-1", Runtime: "java17", Architecture: "x86_64", Instrumented: true},
		{Region: "us-east-1", Runtime: "python3.12", Architecture: "x86_64", Instrumented: false},
		{Region: "us-east-1", Runtime: "go1.x", Architecture: "x86_64", Instrumented: true}, // unsupported -> no key
	}
	got := instrumentedLayerKeys(functions)
	if !got["us-east-1/java/x86_64"] {
		t.Errorf("java key should be in use, got %v", got)
	}
	if got["us-east-1/python/x86_64"] {
		t.Error("uninstrumented python key must not be reported in use")
	}
	if len(got) != 1 {
		t.Errorf("expected one in-use key, got %v", got)
	}
}

// A traces-only destination (e.g. Jaeger) must disable metric/log export, or the collector hard-fails with
// "Unimplemented ... MetricsService" on every batch. Enabled signals are explicitly set to otlp.
func TestApplyCollectorExportSignals(t *testing.T) {
	tracesOnly := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true}
	tracesCfg := mustRenderCollectorConfig(t, tracesOnly)
	env := map[string]string{}
	applyCollectorExport(env, tracesOnly, tracesCfg)
	if env[EnvTracesExporter] != exporterOTLP {
		t.Errorf("a supported signal must explicitly select otlp, got %q", env[EnvTracesExporter])
	}
	if env[EnvMetricsExporter] != exporterNone || env[EnvLogsExporter] != exporterNone {
		t.Errorf("metrics/logs must be none for a traces-only destination: metrics=%q logs=%q", env[EnvMetricsExporter], env[EnvLogsExporter])
	}
	if env[EnvCollectorConfigURI] != tracesCfg {
		t.Errorf("collector config URI not set: %v", env)
	}
	if !collectorExportAsDesired(env, tracesOnly, tracesCfg) {
		t.Error("collectorExportAsDesired should be true right after applyCollectorExport")
	}

	// A full backend enables all three; changing supported signals must fail the skip-check (forces converge).
	full := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true, Metrics: true, Logs: true}
	fullCfg := mustRenderCollectorConfig(t, full)
	if collectorExportAsDesired(env, full, fullCfg) {
		t.Error("skip-check must detect that the destination now supports metrics/logs and re-instrument")
	}
	applyCollectorExport(env, full, fullCfg)
	if env[EnvMetricsExporter] != exporterOTLP {
		t.Errorf("metrics must select otlp once the destination accepts metrics: %v", env)
	}
	if env[EnvLogsExporter] != exporterOTLP {
		t.Errorf("logs must select otlp once the destination accepts logs: %v", env)
	}
}

// The odigos-namespaced collector vars are no longer set. A function still carrying them was instrumented by
// an earlier version and must be reported as not-as-desired exactly once, so the next reconcile strips them
// rather than leaving variables behind that nothing reads.
func TestLegacyOdigosCollectorVarsForceReinstrument(t *testing.T) {
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true}
	cfg := mustRenderCollectorConfig(t, dest)
	env := map[string]string{}
	applyCollectorExport(env, dest, cfg)
	if !collectorExportAsDesired(env, dest, cfg) {
		t.Fatal("freshly applied env should be as desired")
	}
	for _, legacy := range []string{EnvCollectorEndpoint, EnvCollectorInsecure} {
		withLegacy := map[string]string{}
		for k, v := range env {
			withLegacy[k] = v
		}
		withLegacy[legacy] = "collector:4317"
		if collectorExportAsDesired(withLegacy, dest, cfg) {
			t.Errorf("leftover %s must force a re-instrument", legacy)
		}
		applyCollectorExport(withLegacy, dest, cfg)
		if _, ok := withLegacy[legacy]; ok {
			t.Errorf("%s must be deleted on re-instrument", legacy)
		}
	}
}

func mustRenderCollectorConfig(t *testing.T, dest domaindest.OTLPTarget) string {
	t.Helper()
	cfg, err := renderCollectorConfig(dest, nil)
	if err != nil {
		t.Fatalf("renderCollectorConfig: %v", err)
	}
	return cfg
}

// reconcileCustomerEdits must capture a value the customer set on a shared, instrumentor-owned key AFTER
// first instrument (so rollback restores it instead of deleting it), while NOT mistaking our own current
// value for a customer edit.
func TestReconcileCustomerEdits(t *testing.T) {
	desired := map[string]string{EnvServiceName: "our-svc", EnvTracesExporter: exporterOTLP}

	// Customer set OTEL_SERVICE_NAME themselves after we instrumented; the original snapshot said absent (nil).
	m := managedManifest{PrevEnv: map[string]*string{EnvServiceName: nil, EnvTracesExporter: nil}}
	reconcileCustomerEdits(&m, map[string]string{EnvServiceName: "customer-edit", EnvTracesExporter: exporterOTLP}, desired)
	if got := m.PrevEnv[EnvServiceName]; got == nil || *got != "customer-edit" {
		t.Errorf("PrevEnv[%s] = %v, want captured %q", EnvServiceName, got, "customer-edit")
	}
	// The exporter live value equals what we set (ours) — must NOT be captured as a customer edit.
	if got := m.PrevEnv[EnvTracesExporter]; got != nil {
		t.Errorf("PrevEnv[%s] = %q, want nil (our own value, not a customer edit)", EnvTracesExporter, *got)
	}

	// Already-recorded customer value is left as-is (idempotent).
	m2 := managedManifest{PrevEnv: map[string]*string{EnvServiceName: ptr("customer-edit")}}
	reconcileCustomerEdits(&m2, map[string]string{EnvServiceName: "customer-edit"}, desired)
	if got := m2.PrevEnv[EnvServiceName]; got == nil || *got != "customer-edit" {
		t.Errorf("PrevEnv[%s] = %v, want unchanged %q", EnvServiceName, got, "customer-edit")
	}
}

// stripLegacyLoaderValue rebuilds a legacy (pre-marker) append-style loader var's snapshot to the
// customer's ORIGINAL value so rollback does not leave our loader addition stranded (which, after the
// layer is stripped, points at files that no longer exist and can break the function).
func TestStripLegacyLoaderValue(t *testing.T) {
	// Customer had their own NODE_OPTIONS; we appended ours. Rollback must restore just the customer's.
	prev := map[string]*string{EnvNodeOptions: ptr("ignored")}
	env := map[string]string{EnvNodeOptions: "--enable-source-maps " + nodeImportOption}
	stripLegacyLoaderValue(prev, env, EnvNodeOptions, nodeImportOption, " ")
	if got := prev[EnvNodeOptions]; got == nil || *got != "--enable-source-maps" {
		t.Errorf("PrevEnv[%s] = %v, want %q", EnvNodeOptions, got, "--enable-source-maps")
	}

	// Only our value present -> recorded as absent so rollback deletes it.
	prev = map[string]*string{EnvPythonPath: ptr("ignored")}
	env = map[string]string{EnvPythonPath: pythonInstrPath}
	stripLegacyLoaderValue(prev, env, EnvPythonPath, pythonInstrPath, ":")
	if got, ok := prev[EnvPythonPath]; !ok || got != nil {
		t.Errorf("PrevEnv[%s] = (%v, ok=%v), want (nil, true)", EnvPythonPath, got, ok)
	}

	// Key absent from the function (handler-mode legacy) -> dropped so rollback leaves it alone.
	prev = map[string]*string{EnvJavaToolOptions: nil}
	stripLegacyLoaderValue(prev, map[string]string{}, EnvJavaToolOptions, javaAgentOption, " ")
	if _, ok := prev[EnvJavaToolOptions]; ok {
		t.Errorf("PrevEnv[%s] should be dropped when the key is absent", EnvJavaToolOptions)
	}
}

func TestRevisionConflict(t *testing.T) {
	if revisionConflict(nil) {
		t.Error("nil error should not be a revision conflict")
	}
	if revisionConflict(fmt.Errorf("other")) {
		t.Error("unrelated error should not be a revision conflict")
	}
	if !revisionConflict(&lambdatypes.PreconditionFailedException{Message: aws.String("revision mismatch")}) {
		t.Error("PreconditionFailedException should be a revision conflict")
	}
}

func TestPublishedLayerName(t *testing.T) {
	if got := publishedLayerName("java", "arm64"); got != "odigos-otel-java-arm64" {
		t.Errorf("publishedLayerName = %q", got)
	}
}

func TestInstrumentedAsDesiredUsesReportedServiceName(t *testing.T) {
	inst := &LambdaInstrumentor{}
	fn := store.LambdaFunction{FunctionName: "aws-fn", Runtime: "python3.12"}
	dest := domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true}
	layerARN := "arn:aws:lambda:us-east-1:0:layer:odigos-otel-python-x86_64:1"
	cfg := mustRenderCollectorConfig(t, dest)
	env := map[string]string{
		store.EnvOdigosManaged: `{"v":1}`,
		EnvServiceName:         "custom-service",
		EnvMetricsExporter:     exporterNone,
		EnvLogsExporter:        exporterNone,
		EnvCollectorConfigURI:  cfg,
		store.EnvExecWrapper:   execWrapperInstrument,
	}
	layers := []string{layerARN}

	applySignalActivation(env, dest, "python")
	if !inst.instrumentedAsDesired(false, fn, env, layers, layerARN, dest, false, "custom-service", cfg, "") {
		t.Error("expected converged with custom reported service name")
	}
	if inst.instrumentedAsDesired(false, fn, env, layers, layerARN, dest, false, "other-service", cfg, "") {
		t.Error("expected not converged when reported service name override changed")
	}
	env[EnvServiceName] = "aws-fn"
	if inst.instrumentedAsDesired(false, fn, env, layers, layerARN, dest, false, "custom-service", cfg, "") {
		t.Error("expected not converged when live env still has stale service name")
	}
}

// TestIaCMarkerIsAttributionNotOwnership pins the split the two markers exist to express, from the side that
// is dangerous to get wrong.
//
// A function an Odigos IaC artifact instrumented must READ as instrumented — that is the whole point of the
// marker, and reporting such a function as "awaiting your deployment" is the bug this pair of variables was
// introduced to fix. But it must never become MUTABLE by that fact: the IaC marker carries no snapshot of a
// pre-Odigos state, so a rollback keyed off it would strip a layer and env vars with nothing to restore,
// leaving the customer's own deployment definition and the live function permanently disagreeing.
func TestIaCMarkerIsAttributionNotOwnership(t *testing.T) {
	const iacLayer = "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:6"
	env := map[string]string{
		store.EnvOdigosIaC:    `{"v":1,"source":"cdk","version":"v0.0.11","layer":"` + iacLayer + `"}`,
		EnvServiceName:        "checkout",
		store.EnvExecWrapper:  execWrapperInstrument,
		EnvCollectorConfigURI: "yaml:{}",
	}

	if !store.OdigosInstrumented(env) {
		t.Error("a function instrumented by an Odigos IaC artifact reads as untouched, so it is reported as awaiting a deployment that already happened")
	}
	if got := recordedLayer(env); got != iacLayer {
		t.Errorf("recordedLayer = %q, want %q — without it the observer falls back to a resolver manual mode has no permission to call, and reports a pinned version as drift", got, iacLayer)
	}

	i := &LambdaInstrumentor{}
	if _, owned := i.ownership(context.Background(), store.LambdaFunction{FunctionName: "checkout", Region: "us-east-1"}, env); owned {
		t.Error("the IaC marker granted ownership, so rollback would strip instrumentation it has no snapshot to restore")
	}

	// Both markers on one function: the connector took over a function IaC had instrumented. Its own record
	// describes what is applied now, so it wins the layer question.
	const ownedLayer = "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:9"
	env[store.EnvOdigosManaged] = `{"v":1,"layer":"` + ownedLayer + `"}`
	if got := recordedLayer(env); got != ownedLayer {
		t.Errorf("recordedLayer = %q, want the ownership marker's %q when both are present", got, ownedLayer)
	}
	if _, owned := i.ownership(context.Background(), store.LambdaFunction{FunctionName: "checkout", Region: "us-east-1"}, env); !owned {
		t.Error("a function carrying the connector's own marker is not owned, so it can never be rolled back")
	}
}
