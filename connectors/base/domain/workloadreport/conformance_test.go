package workloadreport

import (
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var driftSince = time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)

// observedWorkload is a manual-mode workload the customer has marked, enabled and instrumented, whose
// conformance has been observed as correct. Every case below varies one observed fact from here.
func observedWorkload() api.DiscoveredWorkload {
	w := manualWorkload()
	w.Instrumented = true
	w.Conformance = api.ConformanceConformant
	w.ConformanceSince = driftSince
	return w
}

// TestConformanceStateMatrix walks the rows conformance observation adds to §9's table — the ones marker
// presence alone could not express.
//
// The load-bearing column is again `state`: central-ui decodes current.state through a closed set, so drift has
// to arrive as Error with its detail on stage/issue rather than as a new state string, which would render as
// Unknown and be worse than the status quo.
func TestConformanceStateMatrix(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(*api.DiscoveredWorkload)
		state        string
		instrumented bool
		stage        string
		issueCodes   []string
		wantSince    bool
	}{
		{
			name:         "observed conformant",
			mutate:       func(*api.DiscoveredWorkload) {},
			state:        api.WorkloadStateInstrumented,
			instrumented: true,
			stage:        api.PhaseInstrumented,
		},
		{
			// The false green this whole pass exists to close: the marker is present, so the workload used to
			// report Instrumented, while the layer that does the actual work is gone.
			name: "marker present but instrumentation partial",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Conformance = api.ConformancePartial
				w.MissingComponents = []string{"instrumentationLayer"}
			},
			state:        "Error",
			instrumented: true,
			stage:        "InstrumentationDrift",
			issueCodes:   []string{"InstrumentationPartiallyApplied"},
			wantSince:    true,
		},
		{
			// A change still being applied looks exactly like a partial application. It must win, or every
			// customer deploy would flash a drift error while it lands.
			name: "settling outranks a partial verdict",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Conformance = api.ConformancePartial
				w.MissingComponents = []string{"instrumentationLayer"}
				w.Settling = true
			},
			state:        api.WorkloadStateInstrumenting,
			instrumented: true,
			stage:        "VerifyingCustomerChanges",
		},
		{
			// Nothing applied yet: Absent is not an error, and the workload is simply waiting for the customer.
			name: "conformance absent, awaiting the customer",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Instrumented = false
				w.Conformance = api.ConformanceAbsent
				w.WaitingFor = api.WaitingForCustomer
			},
			state:      api.WorkloadStateNotInstrumented,
			stage:      "Waiting For Customer",
			issueCodes: []string{"Waiting For Customer"},
			wantSince:  true,
		},
		{
			// A read failure is NOT drift. The last confirmed verdict keeps being reported and the failure rides
			// alongside as a transient issue — telling a customer their deployment broke because Odigos could not
			// call the API would be blaming them for our outage.
			name: "observation failed over a conformant workload",
			mutate: func(w *api.DiscoveredWorkload) {
				w.ObservationError = "AccessDenied: lambda:ListFunctions"
			},
			state:        api.WorkloadStateInstrumented,
			instrumented: true,
			stage:        api.PhaseInstrumented,
			issueCodes:   []string{"InstrumentationObservationFailed"},
		},
		{
			// Both: the workload is drifted AND the latest read failed. Both travel, because suppressing either
			// hides half of what the operator needs.
			name: "observation failed over a drifted workload",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Conformance = api.ConformancePartial
				w.MissingComponents = []string{"serviceName"}
				w.ObservationError = "throttled"
			},
			state:        "Error",
			instrumented: true,
			stage:        "InstrumentationDrift",
			issueCodes:   []string{"InstrumentationPartiallyApplied", "InstrumentationObservationFailed"},
			wantSince:    true,
		},
		{
			// Unknown is what a type with no observer reports. It must NOT read as drift: nothing has been
			// observed, so the report falls back to what marker presence can say, exactly as before this wave.
			name: "never observed",
			mutate: func(w *api.DiscoveredWorkload) {
				w.Conformance = api.ConformanceUnknown
				w.ConformanceSince = time.Time{}
			},
			state:        api.WorkloadStateInstrumented,
			instrumented: true,
			stage:        api.PhaseInstrumented,
		},
		{
			// Source gone outranks drift: the remedy is "remove it", not "fix it", so relabelling this as drift
			// would send the customer to repair instrumentation they are about to delete.
			name: "source disabled outranks a partial verdict",
			mutate: func(w *api.DiscoveredWorkload) {
				w.SourceEnabled = false
				w.Conformance = api.ConformancePartial
				w.MissingComponents = []string{"instrumentationLayer"}
			},
			state:        "Error",
			instrumented: true,
			stage:        "SourceDisabledButInstrumented",
			issueCodes:   []string{"SourceDisabledButInstrumented"},
			wantSince:    true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := observedWorkload()
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
			var codes []string
			for _, i := range m.Issues {
				codes = append(codes, i.Code)
			}
			if strings.Join(codes, ",") != strings.Join(c.issueCodes, ",") {
				t.Errorf("issue codes = %v, want %v", codes, c.issueCodes)
			}
			for _, i := range m.Issues {
				if i.Message == "" {
					t.Errorf("issue %q has no message; it is the only explanation a client receives", i.Code)
				}
			}
			if len(c.issueCodes) > 0 && c.wantSince {
				if got := m.Issues[0].Since; got == nil {
					t.Error("the leading issue must carry `since`; how long a state has held is what makes it actionable")
				} else if *got != driftSince.Format(time.RFC3339) {
					t.Errorf("issue.since = %q, want %q", *got, driftSince.Format(time.RFC3339))
				}
			}
		})
	}
}

// TestAutomaticModeIgnoresConformance is the regression guard for the other mode. In automatic mode a partial
// application is a moment in time — the next reconcile converges it — so surfacing it as an error would fire on
// every workload mid-instrument and turn a self-healing connector into a wall of alerts. Only manual mode, where
// nothing converges anything, turns it into something a customer must resolve.
func TestAutomaticModeIgnoresConformance(t *testing.T) {
	for _, conf := range []api.Conformance{api.ConformancePartial, api.ConformanceAbsent, api.ConformanceUnknown} {
		w := observedWorkload()
		w.ManagementMode = spec.ModeAutomatic
		w.Conformance = conf
		w.MissingComponents = []string{"instrumentationLayer"}

		m := ToModel(w)
		if m.Current.State != api.WorkloadStateInstrumented {
			t.Errorf("conformance %q: state = %q, want %q (automatic mode converges this itself)",
				conf, m.Current.State, api.WorkloadStateInstrumented)
		}
		if len(m.Issues) != 0 {
			t.Errorf("conformance %q: issues = %d, want none", conf, len(m.Issues))
		}
	}

	// Settling likewise: an automatic-mode workload mid-apply is already described by its instrumentation phase,
	// which the instrumentor writes. Reporting the customer-facing "verifying your changes" there would credit
	// the customer with a change Odigos made.
	w := observedWorkload()
	w.ManagementMode = spec.ModeAutomatic
	w.Settling = true
	if got := ToModel(w).Progress.Stage; got == "VerifyingCustomerChanges" {
		t.Error("automatic mode must not report VerifyingCustomerChanges; the connector made the change, not the customer")
	}
}

// TestDriftMessageNamesComponents pins the one thing a manual-mode drift report has to deliver: where to look.
// The customer edits their own deployment definition, so a message that says "drift detected" and nothing else
// is unactionable.
func TestDriftMessageNamesComponents(t *testing.T) {
	w := observedWorkload()
	w.Conformance = api.ConformancePartial
	w.MissingComponents = []string{"instrumentationLayer", "serviceName"}

	msg := *ToModel(w).Progress.Message
	for _, want := range []string{
		componentLabels["instrumentationLayer"],
		componentLabels["serviceName"],
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("drift message %q does not name %q", msg, want)
		}
	}

	// A component the base has no label for still reaches the customer as its raw name rather than being
	// dropped: degraded prose beats a message that silently omits the only actionable detail.
	w.MissingComponents = []string{"somethingNewAProviderAdded"}
	if msg := *ToModel(w).Progress.Message; !strings.Contains(msg, "somethingNewAProviderAdded") {
		t.Errorf("an unlabelled component must still be named, got %q", msg)
	}

	// A Partial verdict that arrives with no components at all still produces a usable sentence rather than a
	// dangling one.
	w.MissingComponents = nil
	if msg := *ToModel(w).Progress.Message; msg == "" || strings.Contains(msg, "()") {
		t.Errorf("a component-less Partial verdict must still read as a sentence, got %q", msg)
	}
}

// TestEveryComponentHasALabel is the check that makes centralizing the component vocabulary worth it. Both
// sides live in the base now, so this compares the real declaration against the real label map — unlike the
// provider-side version it replaces, which asserted a duplicated copy of the map against itself and passed
// happily with a label deleted.
//
// A component with no label still reaches the customer, as its raw identifier ("agentStartupDependency"), which
// reads as a bug in Odigos rather than a fact about their deployment.
func TestEveryComponentHasALabel(t *testing.T) {
	if len(api.InstrumentationComponents) == 0 {
		t.Fatal("no components declared; this test has stopped checking anything")
	}
	for _, c := range api.InstrumentationComponents {
		label, ok := componentLabels[c]
		if !ok {
			t.Errorf("component %q has no customer-facing label", c)
			continue
		}
		// The label is prose spliced into a sentence, so it must not be the identifier echoed back.
		if label == c || label == "" {
			t.Errorf("component %q has a placeholder label %q", c, label)
		}
	}
	// The other direction. Keying the map on the constants makes a stale label impossible — rename a constant
	// and its key renames with it — but a label added as a raw STRING would sit here labelling nothing while
	// the component it was meant for stays unlabelled. That is the only way the two can drift now, so it is
	// the one worth asserting.
	declared := make(map[string]bool, len(api.InstrumentationComponents))
	for _, c := range api.InstrumentationComponents {
		declared[c] = true
	}
	for c := range componentLabels {
		if !declared[c] {
			t.Errorf("label for %q, which no api.Component* constant declares; label the constant, not a literal", c)
		}
	}
}
