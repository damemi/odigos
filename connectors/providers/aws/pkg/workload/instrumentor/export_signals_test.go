package instrumentor

import (
	"testing"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func TestDestinationSignalsConvergeAcrossTransitions(t *testing.T) {
	env := map[string]string{}
	for from := 1; from < 8; from++ {
		for to := 1; to < 8; to++ {
			target := func(bits int) domaindest.OTLPTarget {
				return domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: bits&1 != 0, Metrics: bits&2 != 0, Logs: bits&4 != 0}
			}
			old, next := target(from), target(to)
			applyCollectorExport(env, old, mustRenderCollectorConfig(t, old))
			cfg := mustRenderCollectorConfig(t, next)
			applyCollectorExport(env, next, cfg)
			if !collectorExportAsDesired(env, next, cfg) || !next.SignalConfigurationMatches(env, "nodejs") {
				t.Fatalf("transition %d -> %d did not converge: %v", from, to, env)
			}
		}
	}
}

func TestCollectionSummaryListsSupportedSignals(t *testing.T) {
	routing := domaindest.Routing{Found: true, Outcome: domaindest.RoutingDirect, Target: domaindest.OTLPTarget{Traces: true, Metrics: true, Logs: true}}
	for _, note := range []*baseapi.WorkloadRoutingNote{lambdaSignalRoutingNote(routing, "ruby3.3"), fargateSignalRoutingNote(routing, []containerTarget{{recipe: agentRecipe{lang: LangRuby}}})} {
		if note == nil || note.Code != "collected-signals" || note.Transient || note.Message != "Collecting traces" {
			t.Fatalf("unsupported Ruby signals not reported: %+v", note)
		}
	}
	for _, c := range []struct {
		target domaindest.OTLPTarget
		want   string
	}{
		{domaindest.OTLPTarget{Logs: true, Metrics: true, Traces: true}, "Collecting logs, metrics and traces"},
		{domaindest.OTLPTarget{Metrics: true, Traces: true}, "Collecting metrics and traces"},
		{domaindest.OTLPTarget{Traces: true}, "Collecting traces"},
		{domaindest.OTLPTarget{Metrics: true}, "Collecting metrics"},
		{domaindest.OTLPTarget{Logs: true}, "Collecting logs"},
		{domaindest.OTLPTarget{}, "No supported signals enabled"},
	} {
		routing.Target = c.target
		routing.Target.Profiles = true
		for _, note := range []*baseapi.WorkloadRoutingNote{workloadRoutingNote(routing), lambdaSignalRoutingNote(routing, "python3.12"), fargateSignalRoutingNote(routing, []containerTarget{{recipe: agentRecipe{lang: LangRuby}}, {recipe: agentRecipe{lang: LangNodeJS}}})} {
			if note == nil || note.Code != "collected-signals" || note.Message != c.want {
				t.Fatalf("note = %+v, want %q", note, c.want)
			}
		}
	}
}
