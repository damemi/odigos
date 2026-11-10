package instrumentor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	dest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

func TestAgentCoreInstrumentationArtifactsMatchPlans(t *testing.T) {
	plans, err := DesiredAgentCorePlans()
	if err != nil {
		t.Fatalf("DesiredAgentCorePlans: %v", err)
	}
	if len(plans) != 6 {
		t.Fatalf("got %d AgentCore plans, want one per language and distro plus one adopt plan per language (6)", len(plans))
	}
	assertArtifactsMatchPlans(t, agentcore.Kind, plans)
}

func TestAgentCoreArtifactCoverageIsNotVacuous(t *testing.T) {
	plans, err := DesiredAgentCorePlans()
	if err != nil {
		t.Fatal(err)
	}
	// Dropping the X-Ray propagator would orphan every invocation from AgentCore's trace context.
	assertCoverageCatchesDrift(t, agentcore.Kind, plans, "tracecontext,baggage,xray", "tracecontext,baggage")
	// A route B marker that lost its mode would be judged against the package recipe.
	assertCoverageCatchesDrift(t, agentcore.Kind, plans, "mode", "mood")
}

// TestAgentCoreArtifactsWireTheMarkerToTheIaCVariable: the IaC marker identifies customer-owned instrumentation;
// written into ODIGOS_MANAGED it would claim a connector rollback snapshot that does not exist.
func TestAgentCoreArtifactsWireTheMarkerToTheIaCVariable(t *testing.T) {
	artifacts := awsInstrumentationArtifacts(t, agentcore.Kind)
	for _, language := range []string{"python", "nodejs"} {
		byLang := map[string]plan.Artifact{}
		for id, a := range artifacts {
			if a.Language == language {
				byLang[id] = a
			}
		}
		for _, payload := range []string{agentCoreIaCMarkerValue(agentCoreTokenDistro), agentCoreIaCMarkerValue(language + "-agentcore-adot")} {
			if problems := plan.MarkerAttachmentProblems(byLang, payload, store.EnvOdigosIaC, store.EnvOdigosManaged); len(problems) > 0 {
				t.Errorf("%s: %s", language, strings.Join(problems, "\n  - "))
			}
		}
	}
}

// TestAgentCoreManualPlanResolvesToTheObservedRecipe ties the tokenized documents to the conformance check: resolving
// a plan for a runtime must reproduce, key for key, the environment the observer compares, and the package contract
// must make the runtime eligible for the selected distro.
func TestAgentCoreManualPlanResolvesToTheObservedRecipe(t *testing.T) {
	targets := []dest.OTLPTarget{
		{HTTPEndpoint: "https://otlp.example.com:4318/", Traces: true, Metrics: true, Logs: true},
		{HTTPEndpoint: "http://collector.internal:4318/base", Traces: true},
	}
	headerSets := []map[string]string{nil, {"Authorization": "Bearer a b+c"}}
	for _, language := range []string{"python", "nodejs"} {
		for _, d := range AgentCoreDistros(language) {
			p, err := AgentCoreManualPlan(d)
			if err != nil {
				t.Fatalf("%s: %v", d.Name, err)
			}
			for _, target := range targets {
				for _, headers := range headerSets {
					resolved := agentCoreResolved(d, "billing-agent", target)
					got := map[string]string{}
					for _, e := range p.Fragments[0].Env {
						got[e.Name] = plan.Substitute(e.Value, resolved)
					}
					for name, want := range agentCoreOdigosEnv(d, "billing-agent", target, headers) {
						if name == "OTEL_EXPORTER_OTLP_HEADERS" {
							want = agentCoreTokenHeaders // credentials stay out of generated documents
						}
						if got[name] != want {
							t.Errorf("%s %s headers=%v: %s = %q, observer expects %q", d.Name, target.HTTPEndpoint, headers != nil, name, got[name], want)
						}
					}
					pkg := agentcore.ParsePackage(got[agentcore.PackageEnv])
					if err := agentCoreEligible(agentcore.Runtime{Package: pkg}, d); err != nil {
						t.Errorf("%s: the published package contract is not eligible: %v (%s)", d.Name, err, got[agentcore.PackageEnv])
					}
					var marker map[string]any
					if err := json.Unmarshal([]byte(got[store.EnvOdigosIaC]), &marker); err != nil || marker["distro"] != d.Name || marker["recipe"] != agentCoreRecipeVersion {
						t.Errorf("%s: IaC marker %q", d.Name, got[store.EnvOdigosIaC])
					}
					for _, e := range p.Fragments[0].Env {
						if strings.Contains(got[e.Name], "<") && e.Name != "OTEL_EXPORTER_OTLP_HEADERS" {
							t.Errorf("%s: %s is left unresolved: %q", d.Name, e.Name, got[e.Name])
						}
					}
				}
			}
		}
	}
}

// TestAgentCoreAdoptPlanResolvesToTheObservedRecipe is the adopt route's half of the tie between documents and
// conformance: resolved, the plan is agentCoreADOTEnv key for key, its contract declares mode adopt (which is what
// makes the observer pick that recipe), and the runtime it describes is eligible for ADOT.
func TestAgentCoreAdoptPlanResolvesToTheObservedRecipe(t *testing.T) {
	targets := []dest.OTLPTarget{
		{HTTPEndpoint: "https://otlp.example.com:4318/", Traces: true, Metrics: true, Logs: true},
		{HTTPEndpoint: "http://collector.internal:4318/base", Traces: true},
	}
	for _, language := range []string{"python", "nodejs"} {
		p, err := AgentCoreAdoptPlan(language)
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		d, err := ResolveAgentCoreDistro(language, language+"-agentcore-adot")
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			resolved := agentCoreResolved(d, "billing-agent", target)
			got := map[string]string{}
			for _, e := range p.Fragments[0].Env {
				got[e.Name] = plan.Substitute(e.Value, resolved)
				if e.Value == "" && e.Merge != plan.MergeRemove {
					t.Errorf("%s: %s is empty but not a removal", language, e.Name)
				}
			}
			want := agentCoreADOTEnv(d, "billing-agent", target, map[string]string{"Authorization": "x"})
			want["OTEL_EXPORTER_OTLP_HEADERS"] = agentCoreTokenHeaders
			for name, value := range want {
				if got[name] != value {
					t.Errorf("%s %s: %s = %q, observer expects %q", language, target.HTTPEndpoint, name, got[name], value)
				}
			}
			if got[agentcore.EnabledEnv] != "" || got[agentcore.DistroEnv] != "" || got["OTEL_PROPAGATORS"] != "tracecontext,baggage" {
				t.Errorf("%s: adopt plan keeps the bootstrap switches or the X-Ray propagator", language)
			}
			pkg := agentcore.ParsePackage(got[agentcore.PackageEnv])
			if pkg.Mode != agentCoreAdopt {
				t.Errorf("%s: adopt contract %q does not declare mode adopt", language, got[agentcore.PackageEnv])
			}
			if err := agentCoreEligible(agentcore.Runtime{Package: pkg}, d); err != nil {
				t.Errorf("%s: the adopt contract is not eligible: %v", language, err)
			}
			// Instructions carry the same bytes automatic adoption writes.
			auto := agentcore.Package{Version: 1, Language: language, Distros: []string{d.Name}, Mode: "adopt"}
			if language == "nodejs" {
				auto.ModuleFormat = "commonjs"
			}
			if raw, _ := json.Marshal(auto); string(raw) != got[agentcore.PackageEnv] {
				t.Errorf("%s: adopt contract %s, automatic mode writes %s", language, got[agentcore.PackageEnv], raw)
			}
			for name, value := range got {
				if strings.Contains(value, "<") && name != "OTEL_EXPORTER_OTLP_HEADERS" {
					t.Errorf("%s: %s is left unresolved: %q", language, name, value)
				}
			}
		}
	}
}

// TestAgentCorePlanForFollowsTheDeclaredRoute: the package route is the default description; a runtime whose
// contract declares adopt is described by the adopt plan, the one its conformance is judged against.
func TestAgentCorePlanForFollowsTheDeclaredRoute(t *testing.T) {
	for _, language := range []string{"python", "nodejs"} {
		_, s, f := newAgentCoreTest(language)
		p := NewAgentCorePlanner(s)
		w := baseapi.DiscoveredWorkload{ServiceName: "demo", ExternalID: aws.ToString(f.live.AgentRuntimeArn)}
		result, err := p.PlanFor(context.Background(), w)
		if err != nil || result == nil {
			t.Fatalf("%s: %v", language, err)
		}
		if result.Plan.Activation != language+"-agentcore-adot" {
			t.Errorf("%s: default plan %s, want the package route", language, result.Plan.Key())
		}
		s.records[0].Package = agentcore.ParsePackage(agentCoreAdoptPackageValue(language))
		result, err = p.PlanFor(context.Background(), w)
		if err != nil || result == nil {
			t.Fatalf("%s: %v", language, err)
		}
		if result.Plan.Activation != string(agentCoreSDKADOT) {
			t.Errorf("%s: adopt runtime got plan %s", language, result.Plan.Key())
		}
		for _, ph := range result.Plan.Placeholders {
			if _, ok := result.Resolved[ph.Token]; !ok && ph.Token != agentCoreTokenHeaders {
				t.Errorf("%s: adopt plan token %s is not resolved", language, ph.Token)
			}
		}
	}
}
