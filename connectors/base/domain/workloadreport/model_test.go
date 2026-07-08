package workloadreport

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// The instrumentor records a live phase as it works; it must surface as the progress stage so a
// WorkloadReport reflects real-time progress (source matched -> Verifying -> LayerReady -> Instrumenting
// -> Instrumented).
func TestToModelLiveInstrumentationPhase(t *testing.T) {
	for _, phase := range []string{"Verifying", "LayerReady", "Instrumenting"} {
		w := api.DiscoveredWorkload{
			ServiceName:                "fn",
			Kind:                       "aws.lambda",
			InstrumentationType:        "aws.lambda",
			InstrumentationAvailable:   true,
			InstrumentationImplemented: true,
			SourceMatched:              true,
			SourceEnabled:              true,
			InstrumentationPhase:       phase,
		}
		m := ToModel(w)
		if m.Progress.Stage != phase {
			t.Errorf("phase %q: stage = %q, want %q", phase, m.Progress.Stage, phase)
		}
		// An in-flight phase must report the distinct in-progress state so the UI/CLI show it as
		// progressing, not NotInstrumented/disabled.
		if m.Current.State != api.WorkloadStateInstrumenting {
			t.Errorf("phase %q: state = %q, want %q", phase, m.Current.State, api.WorkloadStateInstrumenting)
		}
	}

	// Settling: the config was applied (flag true) but the async Lambda update hasn't settled, so the phase
	// is still Instrumenting. The in-flight phase must win — show in-progress, not prematurely Instrumented.
	if m := ToModel(api.DiscoveredWorkload{
		ServiceName: "fn", Kind: "aws.lambda", InstrumentationType: "aws.lambda",
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		SourceMatched: true, SourceEnabled: true, Instrumented: true,
		InstrumentationPhase: "Instrumenting",
	}); m.Current.State != api.WorkloadStateInstrumenting || m.Progress.Stage != "Instrumenting" {
		t.Errorf("settling: state=%q stage=%q, want Instrumenting/Instrumenting", m.Current.State, m.Progress.Stage)
	}

	// The terminal Instrumented phase reports the Instrumented state, not the in-progress one.
	if m := ToModel(api.DiscoveredWorkload{
		ServiceName: "fn", Kind: "aws.lambda", InstrumentationType: "aws.lambda",
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		SourceMatched: true, SourceEnabled: true, Instrumented: true,
		InstrumentationPhase: api.PhaseInstrumented,
	}); m.Current.State != api.WorkloadStateInstrumented {
		t.Errorf("instrumented: state = %q, want %q", m.Current.State, api.WorkloadStateInstrumented)
	}

	// A recorded error still overrides the live phase (a failed workload shows Error, not its last phase).
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		InstrumentationPhase:       "Instrumenting",
		InstrumentationError:       "boom",
	}
	if m := ToModel(w); m.Progress.Stage != "Error" {
		t.Errorf("error must override live phase: stage = %q, want Error", m.Progress.Stage)
	}
}

// Drift lifecycle: an instrumented workload (no in-flight phase) derives the terminal Instrumented stage
// from the live flag; if it later loses instrumentation (customer redeploy), the flag flips to false and
// the status falls back to SourceMatched/NotInstrumented rather than a stale "Instrumented".
func TestToModelDriftLifecycle(t *testing.T) {
	base := api.DiscoveredWorkload{
		ServiceName: "fn", Kind: "aws.lambda", InstrumentationType: "aws.lambda",
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		SourceMatched: true, SourceEnabled: true,
	}

	// Steady state: instrumented, no persisted phase -> Instrumented derived from the flag.
	done := base
	done.Instrumented = true
	if m := ToModel(done); m.Current.State != api.WorkloadStateInstrumented || m.Progress.Stage != api.PhaseInstrumented {
		t.Errorf("instrumented: state=%q stage=%q, want %q/%q", m.Current.State, m.Progress.Stage, api.WorkloadStateInstrumented, api.PhaseInstrumented)
	}

	// Drifted: lost instrumentation (flag false, no phase) -> NotInstrumented / SourceMatched, not a stale
	// Instrumented.
	drifted := base
	drifted.Instrumented = false
	m := ToModel(drifted)
	if m.Current.State != api.WorkloadStateNotInstrumented {
		t.Errorf("drifted: state = %q, want %q", m.Current.State, api.WorkloadStateNotInstrumented)
	}
	if m.Progress.Stage == api.PhaseInstrumented {
		t.Errorf("drifted: stage must not be a stale %q", api.PhaseInstrumented)
	}
}

// A source-matched workload whose instrumentation type is declared but not built must report
// NotImplemented (a non-error), never as instrumented/working.
func TestToModelNotImplemented(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.fargate-task",
		InstrumentationType:        "aws.fargate-task",
		InstrumentationImplemented: false,
		SourceMatched:              true,
		SourceEnabled:              true,
	}
	m := ToModel(w)
	if m.Current.State != "NotImplemented" {
		t.Errorf("state = %q, want NotImplemented", m.Current.State)
	}
	if m.Progress.Stage != "NotImplemented" {
		t.Errorf("stage = %q, want NotImplemented", m.Progress.Stage)
	}
	if len(m.Issues) != 1 || m.Issues[0].Code != "InstrumentationNotImplemented" {
		t.Errorf("issues = %+v, want one InstrumentationNotImplemented", m.Issues)
	}
}

// A workload whose runtime has no Odigos instrumentation is recorded with an unsupported-runtime error
// and must report as Unsupported (a terminal state) rather than a retryable instrumentation failure.
func TestToModelUnsupportedRuntime(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		InstrumentationError:       api.ErrPrefixUnsupportedRuntime + ` "go1.x": Odigos has no OpenTelemetry instrumentation for this Lambda language`,
	}
	m := ToModel(w)
	if m.Current.State != "Unsupported" {
		t.Errorf("state = %q, want Unsupported", m.Current.State)
	}
	if len(m.Issues) != 1 || m.Issues[0].Code != "UnsupportedRuntime" {
		t.Errorf("issues = %+v, want one UnsupportedRuntime", m.Issues)
	}
}

// A generic instrumentation failure remains a retryable Error (not Unsupported).
func TestToModelGenericErrorStillError(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		InstrumentationError:       "publish layer failed: throttled",
	}
	m := ToModel(w)
	if m.Current.State != "Error" {
		t.Errorf("state = %q, want Error", m.Current.State)
	}
	if len(m.Issues) != 1 || m.Issues[0].Code != "InstrumentationFailed" {
		t.Errorf("issues = %+v, want one InstrumentationFailed", m.Issues)
	}
}

// With a matched source runtime override, the report surfaces detected vs override vs running runtime.
func TestToModelWithRuntimeOverride(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		RuntimeLanguage:            "python",
		RuntimeVersion:             "3.11",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		Overrides: api.SourceOverrides{
			RuntimeLanguage: "java",
			RuntimeVersion:  "17",
		},
	}
	m := ToModel(w)
	if m.Current.Runtime.Override == nil || m.Current.Runtime.Override.Language == nil || *m.Current.Runtime.Override.Language != "java" {
		t.Fatalf("override runtime language = %+v, want java", m.Current.Runtime.Override)
	}
	if m.Current.Runtime.Running.Language == nil || *m.Current.Runtime.Running.Language != "java" {
		t.Fatalf("running runtime language = %+v, want java", m.Current.Runtime.Running.Language)
	}
	if m.Current.RuntimeName == nil || *m.Current.RuntimeName != "java" {
		t.Errorf("deprecated runtime name mirror = %v, want java", m.Current.RuntimeName)
	}
}

// The report fills the runtime language+version (detected == running, plus the deprecated mirrors) and
// the OpenTelemetry sdk type when instrumentation is available.
func TestToModelFillsRuntimeAndSdk(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "frontend",
		Kind:                       "aws.lambda",
		Region:                     "il-central-1",
		RuntimeLanguage:            "java",
		RuntimeVersion:             "17",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		Instrumented:               true,
	}
	m := ToModel(w)

	if got := m.Current.Runtime.Detected; got.Language == nil || *got.Language != "java" || got.Version == nil || *got.Version != "17" {
		t.Errorf("detected runtime = %+v, want java/17", got)
	}
	if got := m.Current.Runtime.Running; got.Language == nil || *got.Language != "java" {
		t.Errorf("running runtime = %+v, want java", got)
	}
	if m.Current.RuntimeName == nil || *m.Current.RuntimeName != "java" || m.Current.RuntimeVersion == nil || *m.Current.RuntimeVersion != "17" {
		t.Errorf("deprecated runtime mirror not filled: name=%v version=%v", m.Current.RuntimeName, m.Current.RuntimeVersion)
	}
	if m.Desired.SdkType == nil || *m.Desired.SdkType != "opentelemetry" {
		t.Errorf("sdkType = %v, want opentelemetry", m.Desired.SdkType)
	}
	if m.Region == nil || *m.Region != "il-central-1" {
		t.Errorf("region = %v", m.Region)
	}
}

// An implemented type with no source stays a normal discovered workload (no NotImplemented noise).
func TestToModelImplementedDiscovered(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationImplemented: true,
		InstrumentationAvailable:   true,
	}
	m := ToModel(w)
	if m.Current.State != "NotInstrumented" {
		t.Errorf("state = %q, want NotInstrumented", m.Current.State)
	}
	if len(m.Issues) != 0 {
		t.Errorf("issues = %+v, want none", m.Issues)
	}
}
