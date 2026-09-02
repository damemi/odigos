package apimap

import (
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// ToPolicy maps an alert-policy wire body to the repo type, narrowing the string
// map keys back to their typed sources. Id is passed through; the repo keys by
// (scope, scope_key), so 0 is fine for a new scope.
func ToPolicy(p apitypes.Policy) repo.Policy {
	var weights map[repo.SignalSource]int
	if len(p.SignalWeights) > 0 {
		weights = make(map[repo.SignalSource]int, len(p.SignalWeights))
		for k, v := range p.SignalWeights {
			weights[repo.SignalSource(k)] = v
		}
	}
	var lists map[repo.EnricherListKey][]string
	if len(p.EnricherLists) > 0 {
		lists = make(map[repo.EnricherListKey][]string, len(p.EnricherLists))
		for k, v := range p.EnricherLists {
			lists[repo.EnricherListKey(k)] = v
		}
	}
	return repo.Policy{
		ID:            p.Id,
		Name:          p.Name,
		Enabled:       p.Enabled,
		FireAtScore:   p.FireAtScore,
		SignalWeights: weights,
		EnricherLists: lists,
		Scope:         repo.NormalizePolicyScope(repo.PolicyScope(p.Scope)),
		ScopeKey:      p.ScopeKey,
	}
}

// ToLearningPolicy maps a learning-policy wire body to the repo type.
func ToLearningPolicy(p apitypes.LearningPolicy) repo.LearningPolicy {
	conditions := make([]repo.LearningCondition, len(p.Conditions))
	for i, c := range p.Conditions {
		conditions[i] = repo.LearningCondition{
			Type:               repo.LearningConditionType(c.Type),
			MinObservations:    c.MinObservations,
			MinDurationMinutes: c.MinDurationMinutes,
			StableObservations: c.StableObservations,
			StableMinutes:      c.StableMinutes,
		}
	}
	return repo.LearningPolicy{
		Class:      repo.DeviationClass(p.Class),
		Mode:       repo.LearningMode(p.Mode),
		Conditions: conditions,
		MinMatches: p.MinMatches,
		Scope:      repo.NormalizePolicyScope(repo.PolicyScope(p.Scope)),
		ScopeKey:   p.ScopeKey,
	}
}

// ToServiceGuardrail maps a guardrail wire body to the repo type, rebuilding the
// per-rule allowlist and mode maps. An empty scope defaults to service (the only
// scope guardrails use today).
func ToServiceGuardrail(g apitypes.ServiceGuardrail) repo.ServiceGuardrail {
	scope := repo.PolicyScope(g.Scope)
	if scope == "" {
		scope = repo.ScopeService
	}
	out := repo.ServiceGuardrail{
		Scope:       repo.NormalizePolicyScope(scope),
		ScopeKey:    g.ScopeKey,
		RuleLists:   map[repo.RuleKey][]string{},
		RuleModes:   map[repo.RuleKey]repo.RuleMode{},
		RuleOrigins: map[repo.RuleKey]string{},
	}
	for _, r := range g.Rules {
		key := repo.RuleKey(r.Key)
		if len(r.Allowlist) > 0 {
			out.RuleLists[key] = r.Allowlist
		}
		out.RuleModes[key] = repo.NormalizeRuleMode(repo.RuleMode(r.Mode))
		if r.Origin != "" {
			out.RuleOrigins[key] = r.Origin
		}
	}
	return out
}
