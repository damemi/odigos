package instrumentor

import (
	"context"
	"errors"
	"testing"
	"time"

	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

const observedLayerARN = "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:7"

var observeDest = domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true}

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
		EnvCollectorEndpoint:   "collector:4317",
		EnvCollectorInsecure:   "true",
		EnvTracesExporter:      exporterOTLP,
		EnvMetricsExporter:     exporterNone,
		EnvLogsExporter:        exporterNone,
		store.EnvExecWrapper:   execWrapperInstrument,
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

			got := instrumentationDiff(false, observedFn(), env, layers, layerARN, observeDest, false, "checkout")
			if !sameSet(got, c.want) {
				t.Errorf("diff = %v, want %v", got, c.want)
			}
			// The mutating path's predicate must stay exactly "the diff is empty", so observation and
			// instrumentation can never disagree about what "correctly instrumented" means.
			inst := &LambdaInstrumentor{}
			asDesired := inst.instrumentedAsDesired(false, observedFn(), env, layers, layerARN, observeDest, false, "checkout")
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
			EnvCollectorEndpoint:   "collector:4317",
			EnvCollectorInsecure:   "true",
			EnvTracesExporter:      exporterOTLP,
			EnvMetricsExporter:     exporterNone,
			EnvLogsExporter:        exporterNone,
			EnvPythonPath:          pythonInstrPath,
			EnvOtlpProtocol:        otlpProtocolHTTP,
		}
	}
	if !usesLambdaWebAdapter(layers) {
		t.Fatal("test setup: the adapter layer must be recognized, or this exercises handler mode")
	}
	if got := instrumentationDiff(true, fn, base(), layers, observedLayerARN, observeDest, false, "web"); len(got) != 0 {
		t.Errorf("conformant web-server function: diff = %v, want none", got)
	}

	// The loader dropped from PYTHONPATH: the agent is no longer loaded even though everything else survived.
	env := base()
	delete(env, EnvPythonPath)
	if got := instrumentationDiff(true, fn, env, layers, observedLayerARN, observeDest, false, "web"); !sameSet(got, []string{componentAgentLoader}) {
		t.Errorf("missing loader: diff = %v, want [%s]", got, componentAgentLoader)
	}

	// Python's HTTP protocol pin removed: the loader is there but the SDK cannot export.
	env = base()
	delete(env, EnvOtlpProtocol)
	if got := instrumentationDiff(true, fn, env, layers, observedLayerARN, observeDest, false, "web"); !sameSet(got, []string{componentAgentLoaderConfig}) {
		t.Errorf("missing protocol pin: diff = %v, want [%s]", got, componentAgentLoaderConfig)
	}

	// A handler-mode function must NOT be judged by web-server rules either: the exec wrapper is what it needs.
	if got := instrumentationDiff(false, fn, base(), layers, observedLayerARN, observeDest, false, "web"); !sameSet(got, []string{componentExecWrapper}) {
		t.Errorf("handler-mode rules over web-server env: diff = %v, want [%s]", got, componentExecWrapper)
	}
}

// stubResolver stands in for a LayerResolver. It records whether it was consulted at all, which is what proves
// the marker takes precedence rather than merely producing the same answer.
type stubResolver struct {
	arn    string
	err    error
	called bool
}

func (s *stubResolver) Resolve(context.Context, string, string, string) (string, error) {
	s.called = true
	return s.arn, s.err
}

// TestDesiredLayerARNPrefersTheMarker pins the precedence that decides what manual-mode drift MEANS.
//
// The marker records the layer the customer actually pinned from the instructions Odigos gave them, so drift is
// "the function no longer carries what was applied". Asking the resolver first would compare against whatever
// version Odigos currently ships, and then a layer release would report every correctly-instrumented function
// in the account as drifted — announcing an available upgrade as an error.
func TestDesiredLayerARNPrefersTheMarker(t *testing.T) {
	pinned := "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:3"
	newer := "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:9"

	res := &stubResolver{arn: newer}
	o := &LambdaObserver{layers: res, debug: func() bool { return false }, now: time.Now}
	env := map[string]string{store.EnvOdigosManaged: `{"v":1,"layer":"` + pinned + `"}`}

	got, err := o.desiredLayerARN(context.Background(), observedFn(), env)
	if err != nil {
		t.Fatalf("desiredLayerARN: %v", err)
	}
	if got != pinned {
		t.Errorf("desired layer = %q, want the pinned %q (a newer published version is an upgrade, not drift)", got, pinned)
	}
	if res.called {
		t.Error("the resolver must not be consulted when the marker records a layer; that is what makes an upgrade not-drift")
	}

	// A marker with no layer — which a customer's own IaC can produce — falls back to the resolver.
	res = &stubResolver{arn: newer}
	o.layers = res
	got, err = o.desiredLayerARN(context.Background(), observedFn(), map[string]string{store.EnvOdigosManaged: `{"v":1}`})
	if err != nil || got != newer {
		t.Errorf("marker without a layer: got (%q, %v), want (%q, nil)", got, err, newer)
	}
	if !res.called {
		t.Error("the resolver is the fallback for a marker with no layer and must be consulted")
	}

	// Neither source answers: an error, so the caller reports an INCOMPLETE CHECK rather than treating
	// "we could not look up the layer" as "the layer is missing".
	o.layers = &stubResolver{err: errors.New("AccessDenied")}
	if _, err := o.desiredLayerARN(context.Background(), observedFn(), map[string]string{store.EnvOdigosManaged: `{"v":1}`}); err == nil {
		t.Error("an unresolvable layer must return an error, not an empty ARN that reads as absent")
	}
}

// TestObservationSkipsASettlingFunction is §7.3's rule at its source: drift is only ever declared after a
// SETTLED read. A configuration change still landing is indistinguishable from one that landed half-way, so
// recording a verdict mid-flight would both report a drift about to resolve itself and reset the drift clock of a
// genuinely broken workload every time the customer touched it.
func TestObservationSkipsASettlingFunction(t *testing.T) {
	o := &LambdaObserver{layers: &stubResolver{arn: observedLayerARN}, debug: func() bool { return false }, now: time.Now}
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
		obs, ok := o.observation(context.Background(), observedFn(), cur, observeDest, "checkout")
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
	base := desiredPrint(false, fn, observedLayerARN, observeDest, false, "checkout")

	cases := map[string]string{
		"service name":  desiredPrint(false, fn, observedLayerARN, observeDest, false, "renamed"),
		"layer version": desiredPrint(false, fn, "arn:aws:lambda:us-east-1:1:layer:odigos-otel-python-x86_64:9", observeDest, false, "checkout"),
		"debug toggle":  desiredPrint(false, fn, observedLayerARN, observeDest, true, "checkout"),
		"mode":          desiredPrint(true, fn, observedLayerARN, observeDest, false, "checkout"),
		"destination": desiredPrint(false, fn, observedLayerARN,
			domaindest.OTLPTarget{Endpoint: "other:4317", Insecure: true, Traces: true}, false, "checkout"),
		"signals": desiredPrint(false, fn, observedLayerARN,
			domaindest.OTLPTarget{Endpoint: "collector:4317", Insecure: true, Traces: true, Metrics: true}, false, "checkout"),
	}
	for what, got := range cases {
		if got == base {
			t.Errorf("a change to the %s must move the desired print", what)
		}
	}
	// An empty reported name falls back to the function name, exactly as the diff does — otherwise the print
	// would move between two inputs the comparator treats as identical.
	if desiredPrint(false, fn, observedLayerARN, observeDest, false, "") != base {
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
