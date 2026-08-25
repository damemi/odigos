package workloadreport

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var intentAt = time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)

// manualWorkload is a manual-mode workload of an implemented, available type — the baseline every case below
// varies one fact from.
func manualWorkload() api.DiscoveredWorkload {
	return api.DiscoveredWorkload{
		ServiceName:                "checkout",
		Kind:                       "gcp.cloud-run",
		InstrumentationType:        "gcp.cloud-run",
		ManagementMode:             spec.ModeManual,
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		SourceIntentAt:             intentAt,
	}
}

// TestManualModeStateMatrix walks the manual-mode rows the connector can decide today — the ones that need
// only intent plus the ownership-marker-derived instrumented flag. Rows requiring per-component conformance
// (partial application, revision drift, traffic realization) need provider observation and are not reachable
// yet.
//
// The `state` column is the load-bearing assertion: central-ui decodes current.state through a closed set, so
// a state string outside {NotInstrumented, Instrumenting, Instrumented, Error, Unsupported, NotImplemented,
// Unknown} silently renders as Unknown — worse than the status quo. New vocabulary belongs on stage/issue.
func TestManualModeStateMatrix(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(*api.DiscoveredWorkload)
		state        string
		instrumented bool
		stage        string
		issueCode    string
		severity     string
		transient    bool
		wantSince    bool
	}{
		{
			name:      "marked and awaiting the customer's deployment",
			mutate:    func(w *api.DiscoveredWorkload) { w.WaitingFor = api.WaitingForCustomer },
			state:     api.WorkloadStateNotInstrumented,
			stage:     "Waiting for you to apply the instrumentation",
			issueCode: "WaitingForCustomer",
			severity:  "unknown",
			transient: true,
			wantSince: true,
		},
		{
			name: "customer applied it: conformant and instrumented",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Instrumented = true
			},
			state:        api.WorkloadStateInstrumented,
			instrumented: true,
			stage:        api.PhaseInstrumented,
		},
		{
			name: "source disabled while instrumentation is still live",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Instrumented = true
				w.SourceEnabled = false
			},
			state:        "Error",
			instrumented: true,
			stage:        "Source disabled but still instrumented",
			issueCode:    "SourceDisabledButInstrumented",
			severity:     "error",
			wantSince:    true,
		},
		{
			name: "source deleted while instrumentation is still live",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Instrumented = true
				w.SourceMatched = false
				w.SourceEnabled = false
				// No source row, so there is no intent timestamp to report.
				w.SourceIntentAt = time.Time{}
			},
			state:        "Error",
			instrumented: true,
			stage:        "No source but still instrumented",
			issueCode:    "SourceMissingButInstrumented",
			severity:     "error",
			wantSince:    false,
		},
		{
			// A destination is a precondition for the instructions themselves: they must carry an OTLP
			// endpoint, so there is nothing for the customer to apply until one exists. The coordinator sets
			// destination first and this branch must not be relabelled as waiting on the customer.
			name:      "missing destination outranks awaiting the customer",
			mutate:    func(w *api.DiscoveredWorkload) { w.WaitingFor = api.WaitingForDestination },
			state:     api.WorkloadStateNotInstrumented,
			stage:     "Waiting for destination",
			issueCode: "WaitingForDestination",
			severity:  "unknown",
			transient: true,
			wantSince: true,
		},
		{
			// Not marked and not instrumented: an ordinary discovered workload. Manual mode must not make
			// every discovered resource look like it is waiting for something.
			name: "discovered but never marked",
			mutate: func(w *api.DiscoveredWorkload) {
				w.SourceMatched = false
				w.SourceEnabled = false
				w.SourceIntentAt = time.Time{}
			},
			state: api.WorkloadStateNotInstrumented,
			stage: "Discovered",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := manualWorkload()
			c.mutate(&w)
			m := ToModel(w)

			if m.Current.State != c.state {
				t.Errorf("current.state = %q, want %q", m.Current.State, c.state)
			}
			if m.Current.Instrumented != c.instrumented {
				t.Errorf("current.instrumented = %v, want %v", m.Current.Instrumented, c.instrumented)
			}
			if m.Progress.Stage != c.stage {
				t.Errorf("progress.stage = %q, want %q", m.Progress.Stage, c.stage)
			}
			if c.issueCode == "" {
				if len(m.Issues) != 0 {
					t.Errorf("issues = %d, want none", len(m.Issues))
				}
				return
			}
			if len(m.Issues) != 1 {
				t.Fatalf("issues = %d, want exactly 1", len(m.Issues))
			}
			got := m.Issues[0]
			if got.Code != c.issueCode {
				t.Errorf("issue.code = %q, want %q", got.Code, c.issueCode)
			}
			if got.Severity != c.severity {
				t.Errorf("issue.severity = %q, want %q", got.Severity, c.severity)
			}
			if transient := got.Transient != nil && *got.Transient; transient != c.transient {
				t.Errorf("issue.transient = %v, want %v", transient, c.transient)
			}
			if hasSince := got.Since != nil; hasSince != c.wantSince {
				t.Errorf("issue.since present = %v, want %v", hasSince, c.wantSince)
			}
			if c.wantSince && *got.Since != intentAt.Format(time.RFC3339) {
				t.Errorf("issue.since = %q, want %q", *got.Since, intentAt.Format(time.RFC3339))
			}
			if got.Message == "" {
				t.Error("issue.message is empty; it is the only explanation a client receives")
			}
		})
	}
}

// TestDesiredModeReported pins the wire field a client reads to tell the two modes apart. It already exists on
// the agents-api WorkloadDesiredReport, so reporting it costs no schema change.
func TestDesiredModeReported(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want *string
	}{
		{name: "manual", mode: spec.ModeManual, want: strPtr(spec.ModeManual)},
		{name: "automatic", mode: spec.ModeAutomatic, want: strPtr(spec.ModeAutomatic)},
		// Not enabled for instrumentation in any mode: nil rather than a guess, so a client can distinguish
		// "not applicable" from either mode.
		{name: "no mode resolved", mode: "", want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := manualWorkload()
			w.ManagementMode = c.mode
			got := ToModel(w).Desired.Mode
			switch {
			case c.want == nil && got != nil:
				t.Errorf("desired.mode = %q, want nil", *got)
			case c.want != nil && got == nil:
				t.Errorf("desired.mode = nil, want %q", *c.want)
			case c.want != nil && *got != *c.want:
				t.Errorf("desired.mode = %q, want %q", *got, *c.want)
			}
		})
	}
}

// TestAutomaticModeUnaffectedBySourceGone is the regression guard for the pre-existing behavior: in automatic
// mode a disabled source is a rollback the connector performs itself, so it must NOT be reported as an error.
// Only manual mode, where Odigos cannot act, turns that situation into something the customer must resolve.
func TestAutomaticModeUnaffectedBySourceGone(t *testing.T) {
	for _, matched := range []bool{true, false} {
		w := manualWorkload()
		w.ManagementMode = spec.ModeAutomatic
		w.Instrumented = true
		w.SourceMatched = matched
		w.SourceEnabled = false

		m := ToModel(w)
		if m.Current.State != api.WorkloadStateInstrumented {
			t.Errorf("sourceMatched=%v: state = %q, want %q (automatic mode rolls back on its own)",
				matched, m.Current.State, api.WorkloadStateInstrumented)
		}
		if len(m.Issues) != 0 {
			t.Errorf("sourceMatched=%v: issues = %d, want none", matched, len(m.Issues))
		}
	}
}

// TestManualModeErrorStillOverrides keeps a provider-reported failure authoritative: an unsupported runtime is
// terminal regardless of mode, and must not be masked by the awaiting-customer state.
func TestManualModeErrorStillOverrides(t *testing.T) {
	w := manualWorkload()
	w.WaitingFor = api.WaitingForCustomer
	w.InstrumentationError = api.ErrPrefixUnsupportedRuntime + " cobol"

	m := ToModel(w)
	if m.Current.State != "Unsupported" {
		t.Errorf("state = %q, want Unsupported", m.Current.State)
	}
	if m.Progress.Stage != "Not supported" {
		t.Errorf("stage = %q, want \"Not supported\"", m.Progress.Stage)
	}
}

// TestStalePhaseIgnoredInManualMode is a regression guard. An instrumentor writes a progress phase as it
// works; in manual mode no instrumentor runs for the type, so a phase found there is a leftover from a
// previous automatic-mode life and nothing will ever advance or clear it. Reporting it made the workload look
// permanently busy — state=Instrumenting alongside stage=WaitingForCustomer and instrumented=false — which is
// three mutually contradictory facts in one report.
func TestStalePhaseIgnoredInManualMode(t *testing.T) {
	for _, phase := range []string{api.PhaseVerifying, api.PhaseLayerReady, api.PhaseInstrumenting} {
		w := manualWorkload()
		w.InstrumentationPhase = phase
		w.WaitingFor = api.WaitingForCustomer

		m := ToModel(w)
		if m.Current.State != api.WorkloadStateNotInstrumented {
			t.Errorf("phase %q: state = %q, want %q", phase, m.Current.State, api.WorkloadStateNotInstrumented)
		}
		if m.Progress.Stage != "Waiting for you to apply the instrumentation" {
			t.Errorf("phase %q: stage = %q, want the WaitingForCustomer label", phase, m.Progress.Stage)
		}
	}

	// Automatic mode must be untouched: there, an in-flight phase is real progress by a running instrumentor.
	w := manualWorkload()
	w.ManagementMode = spec.ModeAutomatic
	w.InstrumentationPhase = api.PhaseInstrumenting
	if m := ToModel(w); m.Current.State != api.WorkloadStateInstrumenting {
		t.Errorf("automatic mode: state = %q, want %q", m.Current.State, api.WorkloadStateInstrumenting)
	}
}

// TestWaitingStateNeverReportsBusy generalizes the fix: whatever a workload is waiting on, "waiting" and
// "actively instrumenting" cannot both be true, so the waiting branch asserts the state rather than inheriting
// whatever an earlier branch left behind.
func TestWaitingStateNeverReportsBusy(t *testing.T) {
	for _, waitingFor := range []string{api.WaitingForCustomer, api.WaitingForDestination} {
		w := manualWorkload()
		// A destination removed mid-instrumentation leaves exactly this: automatic mode, a live phase, and a
		// prerequisite now unmet.
		w.ManagementMode = spec.ModeAutomatic
		w.InstrumentationPhase = api.PhaseInstrumenting
		w.WaitingFor = waitingFor

		if m := ToModel(w); m.Current.State != api.WorkloadStateNotInstrumented {
			t.Errorf("waitingFor %q: state = %q, want %q", waitingFor, m.Current.State, api.WorkloadStateNotInstrumented)
		}
	}
}

func strPtr(s string) *string { return &s }
