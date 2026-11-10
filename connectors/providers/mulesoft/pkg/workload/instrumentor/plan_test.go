package instrumentor

import (
	"context"
	"strings"
	"testing"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
	"github.com/odigos-io/odigos/connectors/spec"
)

var manualTypes = []string{store.KindCloudHub2, store.KindRTF}

func TestManualPlanValidates(t *testing.T) {
	for _, typ := range manualTypes {
		for _, metrics := range []bool{true, false} {
			for _, s := range []Signals{AllSignals, {Traces: true}, {Logs: true}, {Metrics: true}, {}} {
				if _, err := ManualPlan(typ, s, metrics); err != nil {
					t.Errorf("%s (signals=%+v metrics=%v): %v", typ, s, metrics, err)
				}
			}
		}
	}
}

// TestInstrumentationArtifactsMatchPlans: the published instructions are what a manual-mode customer applies,
// and nothing converges them afterwards. Every value the plan needs must appear in every delivery variant, or
// a customer following one of them is reported as drifted for following the instructions.
func TestInstrumentationArtifactsMatchPlans(t *testing.T) {
	s := spec.MustGet("mulesoft")
	for _, typ := range manualTypes {
		artifacts := map[string]string{}
		for _, a := range s.InstrumentationArtifacts(typ) {
			artifacts[a.ID] = a.Template + "\n" + strings.Join(a.Steps, "\n")
		}
		full, err := ManualPlan(typ, AllSignals, true)
		if err != nil {
			t.Fatal(err)
		}
		noMetrics, err := ManualPlan(typ, AllSignals, false)
		if err != nil {
			t.Fatal(err)
		}
		if problems := plan.CoverageProblems(artifacts, []plan.Plan{full, noMetrics}); len(problems) > 0 {
			t.Errorf("%s artifacts do not cover the plan:\n  %s", typ, strings.Join(problems, "\n  "))
		}
	}
}

// TestManualValuesEqualTheAutomaticWriter: a customer who applies the rendered instructions must end up with
// exactly what automatic mode writes, for gRPC and for OTLP/HTTP destinations alike.
func TestManualValuesEqualTheAutomaticWriter(t *testing.T) {
	app := store.Application{ExternalID: "dep-1", AppName: "order-api", EnvironmentName: "Production", TargetType: "shared-space"}
	for name, dest := range map[string]domaindest.OTLPTarget{
		"grpc":        {Endpoint: "collector.example.com:4317", Traces: true, Logs: true, Metrics: true},
		"http":        {HTTPEndpoint: "https://otlp.example.com", Traces: true, Logs: true, Metrics: true},
		"traces only": {Endpoint: "collector.example.com:4317", Traces: true},
		"logs only":   {HTTPEndpoint: "https://otlp.example.com", Logs: true},
	} {
		t.Run(name, func(t *testing.T) {
			values := ManualValues(app, "org-1", dest, true, 0.5)
			values[plan.PlaceholderServiceName] = "order-api"
			pl, err := ManualPlan(store.KindCloudHub2, SignalsFor(dest, true), true)
			if err != nil {
				t.Fatal(err)
			}
			auto := DesiredProperties(Workload{
				ServiceName: "order-api", EnvironmentName: "Production", OrgID: "org-1", TargetType: "shared-space",
				Version: version(t, "4.12.0"),
			}, dest, 0.5)
			inPlan := map[string]bool{}
			for _, e := range pl.Fragments[0].Env {
				inPlan[e.Name] = true
			}
			for k := range auto.Props {
				if !inPlan[k] {
					t.Errorf("automatic mode writes %s but the manual plan does not ask for it", k)
				}
			}
			for _, e := range pl.Fragments[0].Env {
				got := plan.Substitute(e.Value, values)
				switch e.Name {
				case store.PropOdigosIaC:
					continue
				case PropResourceAttributes:
					if got != auto.Attributes {
						t.Errorf("resource attributes: manual %q, automatic %q", got, auto.Attributes)
					}
				default:
					if got != auto.Props[e.Name] {
						t.Errorf("%s: manual %q, automatic %q", e.Name, got, auto.Props[e.Name])
					}
				}
			}
		})
	}
}

type planInv struct {
	app  store.Application
	dest domaindest.OTLPTarget
	ok   bool
}

func (p planInv) ApplicationByID(context.Context, string) (store.Application, bool, error) {
	return p.app, true, nil
}
func (p planInv) OTLPExportTarget(context.Context) (domaindest.OTLPTarget, bool, error) {
	return p.dest, p.ok, nil
}

func TestPlannerResolvesTheWorkloadsValues(t *testing.T) {
	inv := planInv{
		app:  store.Application{ExternalID: "dep-1", EnvironmentName: "Sandbox", TargetType: "runtime-fabric"},
		dest: domaindest.OTLPTarget{HTTPEndpoint: "https://otlp.example.com", Traces: true},
		ok:   true,
	}
	p := NewPlanner(inv, "org-1", func() Settings { return Settings{SampleRatio: 1} })
	got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{
		ExternalID: "dep-1", Kind: store.KindRTF, InstrumentationType: store.KindRTF, RuntimeVersion: "4.11.2", Region: "Sandbox",
	})
	if err != nil || got == nil {
		t.Fatalf("PlanFor = %v, %v", got, err)
	}
	for k, want := range map[string]string{
		PlaceholderProtocol:       "HTTP",
		PlaceholderTracesEndpoint: "https://otlp.example.com/v1/traces",
		PlaceholderEnvironment:    "Sandbox",
		PlaceholderOrganization:   "org-1",
		PlaceholderTargetType:     "runtime-fabric",
		PlaceholderSampleRatio:    "1.0",
		PlaceholderApplicationID:  "dep-1",
	} {
		if got.Resolved[k] != want {
			t.Errorf("%s = %q, want %q", k, got.Resolved[k], want)
		}
	}
	for _, f := range got.Plan.Fragments {
		for _, e := range f.Env {
			if e.Name == PropMetricsEnabled {
				t.Error("a Mule 4.11 plan must not include the meter properties")
			}
		}
	}

	for _, w := range []baseapi.DiscoveredWorkload{
		{ExternalID: "x", Kind: store.KindHybrid, RuntimeVersion: "4.12.0"},                                  // no per-app instrumentation
		{ExternalID: "x", Kind: store.KindRTF, InstrumentationType: store.KindRTF, RuntimeVersion: "4.10.3"}, // ineligible
	} {
		if got, _ := p.PlanFor(context.Background(), w); got != nil {
			t.Errorf("PlanFor(%s %s) should be nil", w.Kind, w.RuntimeVersion)
		}
	}
}
