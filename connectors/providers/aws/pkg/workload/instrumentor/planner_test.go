package instrumentor

import (
	"context"
	"testing"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
)

func TestFargatePlanRuntimePrefersOverrideThenAggregateThenUnanimousContainers(t *testing.T) {
	javaAPI := baseapi.WorkloadContainer{Name: "api", Eligible: true, RuntimeLanguage: "java", RuntimeVersion: "21"}
	javaWorker := baseapi.WorkloadContainer{Name: "worker", Eligible: true, RuntimeLanguage: "java", RuntimeVersion: "21"}
	pythonAPI := baseapi.WorkloadContainer{Name: "api", Eligible: true, RuntimeLanguage: "python", RuntimeVersion: "3.12"}
	sidecar := baseapi.WorkloadContainer{Name: "sidecar", Eligible: false, RuntimeLanguage: "python", RuntimeVersion: "3.12"}

	cases := []struct {
		name        string
		w           baseapi.DiscoveredWorkload
		wantLang    string
		wantVersion string
	}{
		{
			name: "source override wins over a stale aggregate and over containers",
			w: baseapi.DiscoveredWorkload{
				RuntimeLanguage: "python",
				RuntimeVersion:  "3.11",
				Overrides:       baseapi.SourceOverrides{RuntimeLanguage: "javascript", RuntimeVersion: "20"},
				Details:         &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{javaAPI}},
			},
			wantLang:    LangNodeJS,
			wantVersion: "20",
		},
		{
			name: "aggregate language is used when no override is set",
			w: baseapi.DiscoveredWorkload{
				RuntimeLanguage: "java",
				RuntimeVersion:  "17",
				Details:         &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{pythonAPI}},
			},
			wantLang:    LangJava,
			wantVersion: "17",
		},
		{
			name: "unanimous eligible containers fill an empty aggregate — the manual-mode path",
			w: baseapi.DiscoveredWorkload{
				Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{javaAPI, javaWorker, sidecar}},
			},
			wantLang:    LangJava,
			wantVersion: "21",
		},
		{
			name: "mixed eligible languages produce no plan language",
			w: baseapi.DiscoveredWorkload{
				Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{javaAPI, pythonAPI}},
			},
		},
		{
			name: "same language and disagreeing versions keep the language and drop the version",
			w: baseapi.DiscoveredWorkload{
				Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{
					{Name: "api", Eligible: true, RuntimeLanguage: "php", RuntimeVersion: "8.2"},
					{Name: "worker", Eligible: true, RuntimeLanguage: "php", RuntimeVersion: "8.3"},
				}},
			},
			wantLang: LangPHP,
		},
		{
			name: "per-container override is what that container is judged against",
			w: baseapi.DiscoveredWorkload{
				Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{
					{Name: "api", Eligible: true, RuntimeLanguage: "python", RuntimeVersion: "3.12", OverrideLanguage: "java", OverrideVersion: "21"},
					{Name: "worker", Eligible: true, RuntimeLanguage: "python", RuntimeVersion: "3.12", OverrideLanguage: "jvm", OverrideVersion: "21"},
				}},
			},
			wantLang:    LangJava,
			wantVersion: "21",
		},
		{
			name: "no details and no aggregate is unknown",
			w:    baseapi.DiscoveredWorkload{},
		},
		{
			name: "ineligible containers alone cannot invent a language",
			w:    baseapi.DiscoveredWorkload{Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{sidecar}}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLang, gotVer := fargatePlanRuntime(c.w)
			if gotLang != c.wantLang || gotVer != c.wantVersion {
				t.Errorf("fargatePlanRuntime = (%q, %q), want (%q, %q)", gotLang, gotVer, c.wantLang, c.wantVersion)
			}
		})
	}
}

func TestSoleInstrumentableContainerRequiresExactlyOneEligible(t *testing.T) {
	api := baseapi.WorkloadContainer{Name: "api", Eligible: true}
	worker := baseapi.WorkloadContainer{Name: "worker", Eligible: true}
	sidecar := baseapi.WorkloadContainer{Name: "sidecar", Eligible: false}

	if got := soleInstrumentableContainer(baseapi.DiscoveredWorkload{}); got != "" {
		t.Errorf("nil details = %q, want empty", got)
	}
	if got := soleInstrumentableContainer(baseapi.DiscoveredWorkload{
		Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{api, sidecar}},
	}); got != "api" {
		t.Errorf("one eligible = %q, want api", got)
	}
	if got := soleInstrumentableContainer(baseapi.DiscoveredWorkload{
		Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{api, worker, sidecar}},
	}); got != "" {
		t.Errorf("two eligible = %q, want empty so the placeholder stays visible", got)
	}
	if got := soleInstrumentableContainer(baseapi.DiscoveredWorkload{
		Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{sidecar}},
	}); got != "" {
		t.Errorf("only ineligible = %q, want empty", got)
	}
}

func TestECSFargatePlanForResolvesLanguageAndSoleContainer(t *testing.T) {
	p := NewECSFargatePlanner(testAgentsImage)

	t.Run("unknown language publishes no plan", func(t *testing.T) {
		got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{})
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}
		if got != nil {
			t.Fatalf("PlanFor = %+v, want nil when language is unknown", got)
		}
	})

	t.Run("go is not a coverable Fargate language and publishes no plan", func(t *testing.T) {
		if CoverableLanguage("go") || CoverableLanguage("") {
			t.Fatal("Go and an empty language must not be coverable on Fargate")
		}
		got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{RuntimeLanguage: "go"})
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}
		if got != nil {
			t.Fatalf("PlanFor = %+v, want nil: Go normalizes to no language and has no env-driven agent", got)
		}
	})

	t.Run("single java container resolves language and container", func(t *testing.T) {
		got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{
			Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{
				{Name: "checkout", Eligible: true, RuntimeLanguage: "java", RuntimeVersion: "21"},
			}},
		})
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}
		if got == nil || got.Plan.Language != LangJava {
			t.Fatalf("plan language = %v, want java", got)
		}
		if got.Resolved[plan.PlaceholderContainer] != "checkout" {
			t.Errorf("resolved container = %q, want checkout", got.Resolved[plan.PlaceholderContainer])
		}
		if got.Resolved[plan.PlaceholderPackageVersion] == "" {
			t.Error("package version must be resolved; customers cannot guess the connector build")
		}
	})

	t.Run("multi-app task leaves the container placeholder unresolved", func(t *testing.T) {
		got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{
			RuntimeLanguage: "java",
			Details: &baseapi.WorkloadDetails{Containers: []baseapi.WorkloadContainer{
				{Name: "api", Eligible: true, RuntimeLanguage: "java"},
				{Name: "worker", Eligible: true, RuntimeLanguage: "java"},
			}},
		})
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}
		if got == nil {
			t.Fatal("unanimous java containers must still produce a plan")
		}
		if _, ok := got.Resolved[plan.PlaceholderContainer]; ok {
			t.Errorf("resolved container = %q, want the placeholder left for the customer to choose", got.Resolved[plan.PlaceholderContainer])
		}
	})

	t.Run("missing runtime version becomes the published placeholder", func(t *testing.T) {
		got, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{RuntimeLanguage: "php"})
		if err != nil {
			t.Fatalf("PlanFor: %v", err)
		}
		if got == nil {
			t.Fatal("php without a version must still produce a plan")
		}
		if !plan.UsesPlaceholder(got.Plan, plan.PlaceholderRuntimeVersion) {
			t.Error("a version-templated agent with no detected version must keep <RUNTIME_VERSION> in the plan")
		}
	})
}
