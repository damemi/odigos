package loop

import (
	"context"
	"fmt"
	"sort"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/insights/internal/interrogation/db/clickhouse"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// customProbe is one probe already configured for a source, with the rule name.
type customProbe struct {
	RuleName string
	Language string
	Target   string
}

// listCustomProbesForSource returns probes from enabled rules that apply to
// the source, skipping the rule object named excludeObject (pass the
// interrogation rule's name to see only probes other rules provide).
func listCustomProbesForSource(ctx context.Context, c client.Client, source clickhouse.SourceKey, excludeObject string) ([]customProbe, error) {
	if c == nil {
		return nil, nil
	}
	var list odigosv1.InstrumentationRuleList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list instrumentation rules: %w", err)
	}

	pw := k8sconsts.PodWorkload{
		Name:      source.WorkloadName,
		Namespace: source.Namespace,
		Kind:      k8sconsts.WorkloadKind(source.WorkloadKind),
	}

	var out []customProbe
	for i := range list.Items {
		rule := &list.Items[i]
		if rule.Name == excludeObject {
			continue
		}
		if rule.Spec.Disabled || rule.Spec.CustomInstrumentations == nil {
			continue
		}
		if !ruleAffectsSource(rule, pw) {
			continue
		}
		ruleLabel := rule.Spec.RuleName
		if ruleLabel == "" {
			ruleLabel = rule.Name
		}
		out = append(out, probesFromCustomInstrumentations(ruleLabel, rule.Spec.CustomInstrumentations)...)
	}

	sortCustomProbes(out)
	return out, nil
}

func sortCustomProbes(out []customProbe) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Language != out[j].Language {
			return out[i].Language < out[j].Language
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].RuleName < out[j].RuleName
	})
}

// ruleAffectsSource matches SourcesScopes for the workload. Language filters are
// ignored here because ClickHouse source keys do not carry programming language;
// language-scoped custom-probe rules are still included when source/namespace match.
func ruleAffectsSource(rule *odigosv1.InstrumentationRule, pw k8sconsts.PodWorkload) bool {
	scopes := rule.Spec.Scopes
	if scopes == nil {
		return true
	}
	if len(scopes.Sources) > 0 {
		matched := false
		for _, s := range scopes.Sources {
			if s == pw {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(scopes.Namespaces) > 0 {
		matched := false
		for _, ns := range scopes.Namespaces {
			if ns == pw.Namespace {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func probesFromCustomInstrumentations(ruleName string, ci *instrumentationrules.CustomInstrumentationsWithMetadata) []customProbe {
	if ci == nil {
		return nil
	}
	var out []customProbe
	for _, p := range ci.Java {
		out = append(out, customProbe{RuleName: ruleName, Language: "java", Target: p.String()})
	}
	for _, p := range ci.Golang {
		out = append(out, customProbe{RuleName: ruleName, Language: "go", Target: formatGolangProbe(p.GolangCustomProbe)})
	}
	for _, p := range ci.Php {
		out = append(out, customProbe{RuleName: ruleName, Language: "php", Target: p.String()})
	}
	for _, p := range ci.Cpp {
		out = append(out, customProbe{RuleName: ruleName, Language: "cpp", Target: p.Signature})
	}
	return out
}

func formatGolangProbe(p instrumentationrules.GolangCustomProbe) string {
	if p.FunctionName != "" {
		return fmt.Sprintf("%s.%s", p.PackageName, p.FunctionName)
	}
	return fmt.Sprintf("%s.(*%s).%s", p.PackageName, p.ReceiverName, p.ReceiverMethodName)
}

// uniqueProbeCount returns distinct language|target probes affecting the source.
func uniqueProbeCount(probes []customProbe) int {
	seen := make(map[string]bool, len(probes))
	for _, p := range probes {
		seen[p.Language+"|"+p.Target] = true
	}
	return len(seen)
}
