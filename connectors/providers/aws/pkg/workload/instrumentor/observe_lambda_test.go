package instrumentor

import (
	"context"
	"testing"
	"time"

	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

const observedLayerARN = "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:7"

var observeDest = domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true}

// observeCollectorCfg is the inline collector configuration observeDest renders to — the value an
// as-desired function carries in OPENTELEMETRY_COLLECTOR_CONFIG_URI.
func observeCollectorCfg() string {
	cfg, err := renderCollectorConfig(observeDest, nil)
	if err != nil {
		panic(err)
	}
	return cfg
}

func observedFn() store.LambdaFunction {
	return store.LambdaFunction{FunctionName: "checkout", Runtime: "python3.12", Region: "us-east-1"}
}

// conformantEnv is a handler-mode function instrumented exactly as desired for observeDest. Every case below
// perturbs one fact from here, so a case that fails to produce a diff is a real gap rather than a mis-built
// baseline — which the first subtest proves by asserting this baseline is clean.
func conformantEnv() map[string]string {
	return map[string]string{
		store.EnvOdigosManaged: `{"v":1,"layer":"` + observedLayerARN + `"}`,
		EnvServiceName:         "checkout",
		EnvCollectorConfigURI:  observeCollectorCfg(),
		EnvTracesExporter:      exporterOTLP,
		"OTEL_SDK_DISABLED":    "false",
		EnvOtlpProtocol:        otlpProtocolHTTP,
		"OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED": "false",
		EnvMetricsExporter:   exporterNone,
		EnvLogsExporter:      exporterNone,
		store.EnvExecWrapper: execWrapperInstrument,
	}
}

// TestInstrumentationDiffNamesWhatIsWrong is the heart of the observer: in manual mode nobody converges a gap,
// so the component list IS the remedy the customer receives. A diff that is merely non-empty is not enough —
// each case asserts the exact set, because a wrong component name sends someone to edit the wrong thing.
func TestInstrumentationDiffNamesWhatIsWrong(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(env map[string]string, layers *[]string, layerARN *string)
		want   []string
	}{
		{
			name:   "instrumented exactly as desired",
			mutate: func(map[string]string, *[]string, *string) {},
			want:   nil,
		},
		{
			// The manual-mode shape: the customer's own IaC applied everything and stamped the IaC marker
			// instead of the connector's ownership marker. It is fully conformant, and the verdict must say so
			// — this reporting as Absent is what made a correctly instrumented CDK stack look untouched.
			name: "instrumented by IaC, attributed by the IaC marker",
			mutate: func(env map[string]string, _ *[]string, _ *string) {
				delete(env, store.EnvOdigosManaged)
				env[store.EnvOdigosIaC] = `{"v":1,"source":"cdk","version":"v0.0.11","layer":"` + observedLayerARN + `"}`
			},
			want: nil,
		},
		{
			// The plan's own headline drift case, and the one marker-presence checking cannot see: the layer was
			// detached by a redeploy while the env (marker included) survived. The function reports as
			// instrumented and produces no telemetry.
			name:   "layer detached, marker survived",
			mutate: func(_ map[string]string, layers *[]string, _ *string) { *layers = nil },
			want:   []string{componentInstrumentationLayer},
		},
		{
			// A plain uninstrumented function: nothing applied, nothing attributable to Odigos. It must report
			// ONLY the marker. Letting the check cascade would list every component — describing a function
			// nobody has instrumented as one whose instrumentation is broken, which in manual mode reads as
			// "your deployment is failing" for a workload that was never touched.
			name: "nothing applied at all",
			mutate: func(env map[string]string, layers *[]string, _ *string) {
				for k := range env {
					delete(env, k)
				}
				*layers = nil
			},
			want: []string{componentOwnershipMarker},
		},
		{
			// The same rule where it bites hardest: a customer's OWN OpenTelemetry setup, complete with a
			// public OTel layer and their own exporter config. Odigos did not apply it and must not claim it —
			// neither as instrumented (Instrumented is marker-derived) nor as drifted.
			name: "customer's own opentelemetry setup",
			mutate: func(env map[string]string, layers *[]string, _ *string) {
				for k := range env {
					delete(env, k)
				}
				env["OTEL_SERVICE_NAME"] = "their-name"
				env[envLegacyOTLPEndpoint] = "https://their-backend:4317"
				env[store.EnvExecWrapper] = "/opt/datadog_wrapper"
				*layers = []string{"arn:aws:lambda:us-east-1:1:layer:AWSOtelPython:5"}
			},
			want: []string{componentOwnershipMarker},
		},
		{
			name: "service name renamed on the source",
			mutate: func(env map[string]string, _ *[]string, _ *string) {
				env[EnvServiceName] = "old-name"
			},
			want: []string{componentServiceName},
		},
		{
			name: "destination endpoint changed",
			mutate: func(env map[string]string, _ *[]string, _ *string) {
				env[EnvCollectorEndpoint] = "old-collector:4317"
			},
			want: []string{componentCollectorExport},
		},
		{
			// The destination gained a signal: the exporter selectors are now stale even though the endpoint
			// matches, so telemetry the backend now accepts is still being suppressed.
			name: "destination gained a signal",
			mutate: func(env map[string]string, _ *[]string, _ *string) {
				env[EnvMetricsExporter] = exporterOTLP
			},
			want: []string{componentCollectorExport},
		},
		{
			name: "exec wrapper stripped",
			mutate: func(env map[string]string, _ *[]string, _ *string) {
				delete(env, store.EnvExecWrapper)
			},
			want: []string{componentExecWrapper},
		},
		{
			name: "legacy sdk-direct export vars left behind",
			mutate: func(env map[string]string, _ *[]string, _ *string) {
				env[envLegacyOTLPEndpoint] = "https://backend:4317"
			},
			want: []string{componentLegacyExportVars},
		},
		{
			name: "two versions of the odigos layer attached",
			mutate: func(_ map[string]string, layers *[]string, _ *string) {
				*layers = append(*layers, "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:6")
			},
			want: []string{componentLayerVersionConflict},
		},
		{
			// An unresolvable desired layer must NOT become a missing layer. The observer reports the check as
			// incomplete instead; claiming drift on the strength of not having looked is manufactured drift, and
			// in manual mode it would tell a customer to go fix something that is not broken.
			name: "desired layer unresolvable",
			mutate: func(_ map[string]string, layers *[]string, layerARN *string) {
				*layerARN = ""
				*layers = nil
			},
			want: nil,
		},
		{
			// Several at once: a customer who applied the marker and nothing else — the exact "false green" the
			// marker-only check reported as healthy.
			name: "marker applied, nothing else",
			mutate: func(env map[string]string, layers *[]string, _ *string) {
				marker := env[store.EnvOdigosManaged]
				for k := range env {
					delete(env, k)
				}
				env[store.EnvOdigosManaged] = marker
				*layers = nil
			},
			want: []string{componentServiceName, componentCollectorExport, componentInstrumentationLayer, componentExecWrapper},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := conformantEnv()
			layers := []string{observedLayerARN}
			layerARN := observedLayerARN
			c.mutate(env, &layers, &layerARN)

			got := instrumentationDiff(false, observedFn(), env, layers, layerARN, observeDest, false, "checkout", observeCollectorCfg(), "")
			if !sameSet(got, c.want) {
				t.Errorf("diff = %v, want %v", got, c.want)
			}
			// The mutating path's predicate must stay exactly "the diff is empty", so observation and
			// instrumentation can never disagree about what "correctly instrumented" means.
			inst := &LambdaInstrumentor{}
			asDesired := inst.instrumentedAsDesired(false, observedFn(), env, layers, layerARN, observeDest, false, "checkout", observeCollectorCfg(), "")
			if asDesired != (len(got) == 0) {
				t.Errorf("instrumentedAsDesired = %v but diff = %v; the two must agree", asDesired, got)
			}
		})
	}
}

// TestInstrumentationDiffWebServerMode covers the other instrumentation mode: a web-adapter function is
// instrumented through the language loader, not the exec wrapper, so judging it by handler-mode rules would
// report every correctly-instrumented web-adapter function as drifted.
func TestInstrumentationDiffWebServerMode(t *testing.T) {
	fn := store.LambdaFunction{FunctionName: "web", Runtime: "python3.12", Region: "us-east-1"}
	layers := []string{observedLayerARN, "arn:aws:lambda:us-east-1:1:layer:LambdaAdapterLayerX86:25"}
	base := func() map[string]string {
		return map[string]string{
			store.EnvOdigosManaged: `{"v":1,"layer":"` + observedLayerARN + `"}`,
			EnvServiceName:         "web",
			EnvCollectorConfigURI:  observeCollectorCfg(),
			EnvTracesExporter:      exporterOTLP,
			"OTEL_SDK_DISABLED":    "false",
			EnvOtlpProtocol:        otlpProtocolHTTP,
			"OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED": "false",
			EnvMetricsExporter: exporterNone,
			EnvLogsExporter:    exporterNone,
			EnvPythonPath:      pythonInstrPath,
		}
	}
	if !usesLambdaWebAdapter(layers) {
		t.Fatal("test setup: the adapter layer must be recognized, or this exercises handler mode")
	}
	if got := instrumentationDiff(true, fn, base(), layers, observedLayerARN, observeDest, false, "web", observeCollectorCfg(), ""); len(got) != 0 {
		t.Errorf("conformant web-server function: diff = %v, want none", got)
	}

	// The loader dropped from PYTHONPATH: the agent is no longer loaded even though everything else survived.
	env := base()
	delete(env, EnvPythonPath)
	if got := instrumentationDiff(true, fn, env, layers, observedLayerARN, observeDest, false, "web", observeCollectorCfg(), ""); !sameSet(got, []string{componentAgentLoader}) {
		t.Errorf("missing loader: diff = %v, want [%s]", got, componentAgentLoader)
	}

	// Python's HTTP protocol pin removed: the loader is there but the SDK cannot export.
	env = base()
	delete(env, EnvOtlpProtocol)
	if got := instrumentationDiff(true, fn, env, layers, observedLayerARN, observeDest, false, "web", observeCollectorCfg(), ""); !sameSet(got, []string{componentCollectorExport}) {
		t.Errorf("missing protocol pin: diff = %v, want [%s]", got, componentCollectorExport)
	}

	// A handler-mode function must NOT be judged by web-server rules either: the exec wrapper is what it needs.
	if got := instrumentationDiff(false, fn, base(), layers, observedLayerARN, observeDest, false, "web", observeCollectorCfg(), ""); !sameSet(got, []string{componentExecWrapper}) {
		t.Errorf("handler-mode rules over web-server env: diff = %v, want [%s]", got, componentExecWrapper)
	}
}

// TestLayerVerdictHonoursThePinnedVersion pins what manual-mode layer drift MEANS, across both the pinned and
// the unpinned case — the two halves of one rule: a function is only ever held to the layer its own applied
// instrumentation recorded, never to the layer Odigos happens to ship today.
//
// The unpinned half is the regression this guards. A marker that records no layer used to fall back to
// resolving one, which answers "the newest published version" — so a customer who pinned an older version, or
// whose IaC artifact predated the recorded layer, was reported as drifted for a version they deliberately
// chose. Every layer release re-broke every such function until it was re-applied.
func TestLayerVerdictHonoursThePinnedVersion(t *testing.T) {
	const (
		pinned  = "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:3"
		foreign = "arn:aws:lambda:us-east-1:1:layer:aws-otel-python-x86_64:5"
	)
	// An IaC marker carrying no layer at all — exactly what an older Odigos CDK release wrote.
	unpinnedMarker := map[string]string{store.EnvOdigosIaC: `{"v":1,"source":"cdk","version":"v0.0.12"}`}

	for _, c := range []struct {
		name   string
		marker map[string]string
		layers []string
		want   []string
	}{
		{
			// The whole point: an Odigos layer IS attached, at a version nothing recorded. There is no version to
			// hold it to, so signature is the honest test and this is conformant.
			name:   "unpinned marker with an odigos layer attached is not drift",
			marker: unpinnedMarker,
			layers: []string{pinned},
			want:   nil,
		},
		{
			name:   "unpinned marker with no odigos layer attached is drift",
			marker: unpinnedMarker,
			layers: []string{foreign},
			want:   []string{componentInstrumentationLayer},
		},
		{
			name:   "unpinned marker with no layers at all is drift",
			marker: unpinnedMarker,
			layers: nil,
			want:   []string{componentInstrumentationLayer},
		},
		{
			// A recorded layer is an identity check, and it stays one: this is how a detached or replaced layer is
			// caught while the marker survives.
			name:   "pinned marker holds the function to that exact version",
			marker: map[string]string{store.EnvOdigosIaC: `{"v":1,"layer":"` + pinned + `"}`},
			layers: []string{observedLayerARN},
			want:   []string{componentInstrumentationLayer},
		},
		{
			name:   "pinned marker satisfied by the version it names",
			marker: map[string]string{store.EnvOdigosIaC: `{"v":1,"layer":"` + pinned + `"}`},
			layers: []string{pinned},
			want:   nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := &LambdaObserver{debug: func() bool { return false }, now: time.Now}
			env := conformantEnv()
			delete(env, store.EnvOdigosManaged) // the marker under test is the only attribution
			for k, v := range c.marker {
				env[k] = v
			}
			cur := lambdatypes.FunctionConfiguration{
				State:            lambdatypes.StateActive,
				LastUpdateStatus: lambdatypes.LastUpdateStatusSuccessful,
				Environment:      &lambdatypes.EnvironmentResponse{Variables: env},
				Layers:           layerList(c.layers),
			}
			obs, ok := o.observation(context.Background(), observedFn(), cur, observeDest, nil,
				enabledLambdaSource{serviceName: "checkout"})
			if !ok {
				t.Fatal("no verdict recorded for a settled function")
			}
			if !sameSet(obs.Missing, c.want) {
				t.Errorf("missing = %v, want %v", obs.Missing, c.want)
			}
			// An unresolvable layer must never surface as an observation ERROR either: the observer no longer looks
			// one up, so there is nothing that can fail.
			if obs.Error != "" {
				t.Errorf("unexpected observation error: %s", obs.Error)
			}
			wantConformance := baseapi.ConformanceConformant
			if len(c.want) > 0 {
				wantConformance = baseapi.ConformancePartial
			}
			if obs.Conformance != wantConformance {
				t.Errorf("conformance = %q, want %q", obs.Conformance, wantConformance)
			}
		})
	}
}

// Manual and managed instrumentation must match the full destination signal set.
func TestExportedSignalsMustMatchDestination(t *testing.T) {
	type sig struct{ traces, metrics, logs bool }

	for _, c := range []struct {
		name  string
		instr sig
		dest  sig
		want  []string
	}{
		{"exact match", sig{true, false, false}, sig{true, false, false}, nil},
		{"traces-only against an all-signal destination", sig{true, false, false}, sig{true, true, true}, []string{componentCollectorExport}},
		{"traces+metrics against an all-signal destination", sig{true, true, false}, sig{true, true, true}, []string{componentCollectorExport}},
		{"all three against an all-signal destination", sig{true, true, true}, sig{true, true, true}, nil},
		// The harmful direction — a signal with no pipeline at the far end.
		{"exports metrics and logs the destination refuses", sig{true, true, true}, sig{true, false, false}, []string{componentCollectorExport}},
		{"exports logs the destination refuses", sig{true, false, true}, sig{true, true, false}, []string{componentCollectorExport}},
		// Not a narrower choice but a collector with no pipeline: it fails to start and the function is silent.
		{"exports nothing at all", sig{false, false, false}, sig{true, true, true}, []string{componentCollectorExport}},
	} {
		t.Run(c.name, func(t *testing.T) {
			liveDest := domaindest.OTLPTarget{
				Endpoint: observeDest.Endpoint, Insecure: observeDest.Insecure,
				Traces: c.instr.traces, Metrics: c.instr.metrics, Logs: c.instr.logs,
			}
			// The live config URI is rendered by the real renderer for what the function exports, so this asserts
			// the rule rather than a hand-written string that could drift from it.
			liveCfg, err := renderCollectorConfig(liveDest, nil)
			if err != nil {
				liveCfg = "yaml:{}" // the no-signal case, which has no renderable document
			}
			env := map[string]string{
				store.EnvOdigosManaged: `{"v":1,"layer":"` + observedLayerARN + `"}`,
				EnvServiceName:         "checkout",
				EnvCollectorConfigURI:  liveCfg,
				store.EnvExecWrapper:   execWrapperInstrument,
			}
			applySignalActivation(env, liveDest, "python")
			if !c.instr.traces {
				env[EnvTracesExporter] = exporterNone
			}
			if !c.instr.metrics {
				env[EnvMetricsExporter] = exporterNone
			}
			if !c.instr.logs {
				env[EnvLogsExporter] = exporterNone
			}

			o := &LambdaObserver{debug: func() bool { return false }, now: time.Now}
			cur := lambdatypes.FunctionConfiguration{
				State:            lambdatypes.StateActive,
				LastUpdateStatus: lambdatypes.LastUpdateStatusSuccessful,
				Environment:      &lambdatypes.EnvironmentResponse{Variables: env},
				Layers:           layerList([]string{observedLayerARN}),
			}
			dest := domaindest.OTLPTarget{
				Endpoint: observeDest.Endpoint, Insecure: observeDest.Insecure,
				Traces: c.dest.traces, Metrics: c.dest.metrics, Logs: c.dest.logs,
			}
			obs, ok := o.observation(context.Background(), observedFn(), cur, dest, nil,
				enabledLambdaSource{serviceName: "checkout"})
			if !ok {
				t.Fatal("no verdict recorded for a settled function")
			}
			if !sameSet(obs.Missing, c.want) {
				t.Errorf("missing = %v, want %v", obs.Missing, c.want)
			}
		})
	}
}

// layerList turns observed layer ARNs into the SDK shape ListFunctions returns them in.
func layerList(arns []string) []lambdatypes.Layer {
	out := make([]lambdatypes.Layer, 0, len(arns))
	for _, a := range arns {
		arn := a
		out = append(out, lambdatypes.Layer{Arn: &arn})
	}
	return out
}

// TestObservationSkipsASettlingFunction is §7.3's rule at its source: drift is only ever declared after a
// SETTLED read. A configuration change still landing is indistinguishable from one that landed half-way, so
// recording a verdict mid-flight would both report a drift about to resolve itself and reset the drift clock of a
// genuinely broken workload every time the customer touched it.
func TestObservationSkipsASettlingFunction(t *testing.T) {
	o := &LambdaObserver{debug: func() bool { return false }, now: time.Now}
	// Deliberately a function whose instrumentation IS broken — the layer is gone — so a skip cannot be confused
	// with "nothing was wrong anyway".
	env := conformantEnv()
	cur := lambdatypes.FunctionConfiguration{
		Environment: &lambdatypes.EnvironmentResponse{Variables: env},
	}

	for _, c := range []struct {
		state       lambdatypes.State
		lastUpdate  lambdatypes.LastUpdateStatus
		wantVerdict bool
	}{
		{lambdatypes.StateActive, lambdatypes.LastUpdateStatusSuccessful, true},
		{lambdatypes.StatePending, "", false},
		{lambdatypes.StateActive, lambdatypes.LastUpdateStatusInProgress, false},
		// A FAILED update has finished, badly. It is not settling, and refusing to judge it would hide a real
		// problem behind "verifying your changes" forever.
		{lambdatypes.StateActive, lambdatypes.LastUpdateStatusFailed, true},
	} {
		cur.State, cur.LastUpdateStatus = c.state, c.lastUpdate
		obs, ok := o.observation(context.Background(), observedFn(), cur, observeDest, nil, enabledLambdaSource{serviceName: "checkout"})
		if ok != c.wantVerdict {
			t.Errorf("state=%q lastUpdateStatus=%q: recorded a verdict = %v, want %v", c.state, c.lastUpdate, ok, c.wantVerdict)
		}
		if !c.wantVerdict {
			continue
		}
		// The settled cases must reach a real verdict, or the assertion above is vacuous: no layers are attached
		// here, so the layer must come back as missing.
		if obs.Conformance != baseapi.ConformancePartial || !sameSet(obs.Missing, []string{componentInstrumentationLayer}) {
			t.Errorf("state=%q: verdict = (%q, %v), want Partial missing the layer", c.state, obs.Conformance, obs.Missing)
		}
	}
}

// TestNextObservationCarriesTheSinceStamp is the drift clock. Re-stamping on every pass is the obvious
// implementation and it silently destroys the only thing that makes a drift report actionable: how long it has
// stood. This asserts the stamp survives repeated passes and moves only when the verdict actually changes.
func TestNextObservationCarriesTheSinceStamp(t *testing.T) {
	first := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)

	drifted := store.LambdaObservation{
		Conformance:   baseapi.ConformancePartial,
		Missing:       []string{componentInstrumentationLayer},
		ObservedPrint: "obs1",
		DesiredPrint:  "des1",
	}

	// First observation of a drift: stamped now.
	stored, write := nextObservation(store.LambdaObservation{}, drifted, first)
	if !write {
		t.Fatal("a first observation must be written")
	}
	if !stored.Since.Equal(first) {
		t.Fatalf("since = %v, want %v", stored.Since, first)
	}

	// Same drift seen again three days later: nothing moved, so no write at all — and the stamp stays put.
	next, write := nextObservation(stored, drifted, later)
	if write {
		t.Error("an unchanged observation must not be rewritten; the fingerprints exist to make that skip safe")
	}
	if !next.Since.Equal(first) {
		t.Errorf("since = %v, want it carried forward as %v (this is what makes 'drifted for 3 days' true)", next.Since, first)
	}

	// The customer fixes it: the verdict flips, so the stamp moves to when the NEW state began.
	fixed := store.LambdaObservation{Conformance: baseapi.ConformanceConformant, ObservedPrint: "obs2", DesiredPrint: "des1"}
	got, write := nextObservation(stored, fixed, later)
	if !write || !got.Since.Equal(later) {
		t.Errorf("verdict flip: write = %v, since = %v, want true and %v", write, got.Since, later)
	}

	// The function changed but the verdict did not (a different component broke): the row is rewritten so the
	// component list is current, and the stamp holds — the workload has been drifted continuously.
	otherDrift := store.LambdaObservation{
		Conformance:   baseapi.ConformancePartial,
		Missing:       []string{componentServiceName},
		ObservedPrint: "obs3",
		DesiredPrint:  "des1",
	}
	got, write = nextObservation(stored, otherDrift, later)
	if !write {
		t.Fatal("a changed component list must be written")
	}
	if !got.Since.Equal(first) {
		t.Errorf("since = %v, want %v: the workload never stopped being drifted", got.Since, first)
	}

	// A record stored without a stamp (written by an older connector) is rewritten once to acquire one, rather
	// than staying permanently undateable.
	unstamped := store.LambdaObservation{Conformance: baseapi.ConformancePartial, ObservedPrint: "obs1", DesiredPrint: "des1"}
	got, write = nextObservation(unstamped, drifted, later)
	if !write || !got.Since.Equal(later) {
		t.Errorf("unstamped record: write = %v, since = %v, want true and %v", write, got.Since, later)
	}
}

// TestObservedPrintTracksInstrumentationOnly pins what the change detector is sensitive to. It must move when
// the instrumentation moves, and NOT move on the marker's prevEnv snapshot — that snapshot holds the customer's
// pre-Odigos values, which change for reasons that are not instrumentation changes and would otherwise force a
// pointless rewrite (and a reset of the drift clock's write-skip) on every unrelated edit.
func TestObservedPrintTracksInstrumentationOnly(t *testing.T) {
	layers := []string{observedLayerARN}
	base := observedPrint(conformantEnv(), layers)

	if same := observedPrint(conformantEnv(), layers); same != base {
		t.Error("the print must be stable for identical input")
	}

	env := conformantEnv()
	env[store.EnvOdigosManaged] = `{"v":1,"layer":"` + observedLayerARN + `","prevEnv":{"PYTHONPATH":"/app"}}`
	if got := observedPrint(env, layers); got != base {
		t.Error("a change to the marker's prevEnv snapshot is not an instrumentation change and must not move the print")
	}

	env = conformantEnv()
	env[store.EnvOdigosManaged] = `{"v":1,"layer":"other-layer"}`
	if got := observedPrint(env, layers); got == base {
		t.Error("the layer recorded in the marker is part of the instrumentation and must move the print")
	}

	for _, mutate := range []func(map[string]string, *[]string){
		func(e map[string]string, _ *[]string) { e[EnvServiceName] = "renamed" },
		func(e map[string]string, _ *[]string) { e[EnvCollectorEndpoint] = "elsewhere:4317" },
		func(e map[string]string, _ *[]string) { delete(e, store.EnvExecWrapper) },
		func(_ map[string]string, l *[]string) { *l = nil },
	} {
		env, ls := conformantEnv(), append([]string(nil), layers...)
		mutate(env, &ls)
		if got := observedPrint(env, ls); got == base {
			t.Error("an instrumentation change must move the observed print")
		}
	}

	// Layer ORDER is not an instrumentation change: AWS returns a set, and treating a reordering as a change
	// would rewrite every row (and defeat the write-skip) for nothing.
	other := "arn:aws:lambda:us-east-1:1:layer:Other:2"
	if a, b := observedPrint(conformantEnv(), []string{observedLayerARN, other}),
		observedPrint(conformantEnv(), []string{other, observedLayerARN}); a != b {
		t.Error("layer order must not move the print; the attached layers are a set, not a sequence")
	}
}

// TestDesiredPrintTracksDesiredInputs is the counterpart: the desired print must move when the thing the
// customer is supposed to apply changes, since that is the other half of "has anything moved since last pass".
func TestDesiredPrintTracksDesiredInputs(t *testing.T) {
	fn := observedFn()
	cfgFor := func(d domaindest.OTLPTarget) string {
		cfg, err := renderCollectorConfig(d, nil)
		if err != nil {
			t.Fatalf("renderCollectorConfig: %v", err)
		}
		return cfg
	}
	base := desiredPrint(false, fn, observedLayerARN, false, "checkout", observeCollectorCfg(), "")

	cases := map[string]string{
		"service name":  desiredPrint(false, fn, observedLayerARN, false, "renamed", observeCollectorCfg(), ""),
		"layer version": desiredPrint(false, fn, "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:9", false, "checkout", observeCollectorCfg(), ""),
		"debug toggle":  desiredPrint(false, fn, observedLayerARN, true, "checkout", observeCollectorCfg(), ""),
		"mode":          desiredPrint(true, fn, observedLayerARN, false, "checkout", observeCollectorCfg(), ""),
		"destination": desiredPrint(false, fn, observedLayerARN, false, "checkout",
			cfgFor(domaindest.OTLPTarget{Endpoint: "other:4317", Insecure: true, Traces: true}), ""),
		"signals": desiredPrint(false, fn, observedLayerARN, false, "checkout",
			cfgFor(domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true, Metrics: true}), ""),
		// The transport and the credential are new inputs the previous print could not see at all.
		"transport": desiredPrint(false, fn, observedLayerARN, false, "checkout",
			cfgFor(domaindest.OTLPTarget{HTTPEndpoint: "https://collector:4318", Traces: true}), ""),
	}
	// A java function whose source selects a different wrapper: the print must move, or the observer reports
	// "nothing changed" on exactly the change the customer just made.
	javaFn := store.LambdaFunction{FunctionName: "checkout", Runtime: "java21", Region: "us-east-1"}
	javaBase := desiredPrint(false, javaFn, observedLayerARN, false, "checkout", observeCollectorCfg(), "")
	cases["java wrapper selection"] = desiredPrint(false, javaFn, observedLayerARN, false, "checkout", observeCollectorCfg(), "otel-stream-handler")
	if cases["java wrapper selection"] == javaBase {
		t.Error("selecting a different java wrapper must move the desired print")
	}
	delete(cases, "java wrapper selection")
	for what, got := range cases {
		if got == base {
			t.Errorf("a change to the %s must move the desired print", what)
		}
	}
	// An empty reported name falls back to the function name, exactly as the diff does — otherwise the print
	// would move between two inputs the comparator treats as identical.
	if desiredPrint(false, fn, observedLayerARN, false, "", observeCollectorCfg(), "") != base {
		t.Error("an empty reported service name must resolve to the function name, as instrumentationDiff does")
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}
