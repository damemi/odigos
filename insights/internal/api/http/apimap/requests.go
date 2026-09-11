package apimap

import (
	"fmt"
	"regexp"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
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
		Scope:            repo.NormalizePolicyScope(scope),
		ScopeKey:         g.ScopeKey,
		RuleLists:        map[repo.RuleKey][]string{},
		RuleModes:        map[repo.RuleKey]repo.RuleMode{},
		RuleOrigins:      map[repo.RuleKey]string{},
		RuleCorrelations: map[repo.RuleKey][]repo.CorrelationSpec{},
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
		if specs := toCorrelationSpecs(r.Correlations); len(specs) > 0 {
			out.RuleCorrelations[key] = specs
		}
	}
	return out
}

// toCorrelationSpecs maps wire correlation specs to their repo form, defaulting
// an empty severity to critical (the field is the per-rule risk of a violation).
func toCorrelationSpecs(specs []apitypes.CorrelationSpec) []repo.CorrelationSpec {
	if len(specs) == 0 {
		return nil
	}
	out := make([]repo.CorrelationSpec, len(specs))
	for i, s := range specs {
		severity := string(s.Severity)
		if severity == "" {
			severity = string(apitypes.SeverityCritical)
		}
		out[i] = repo.CorrelationSpec{
			Name:     s.Name,
			Left:     toCorrelationSelector(s.Left),
			Right:    toCorrelationSelector(s.Right),
			Relation: string(s.Relation),
			Severity: severity,
			Why:      s.Why,
		}
	}
	return out
}

func toCorrelationSelector(sel apitypes.CorrelationSelector) repo.CorrelationSelector {
	return repo.CorrelationSelector{
		Service: sel.Service,
		Span:    sel.Span,
		Attr:    sel.Attr,
		Extract: sel.Extract,
	}
}

// ValidateServiceGuardrail rejects a malformed guardrail body before it is
// mapped and persisted. It focuses on the attribute_correlation rule, whose
// specs carry structure the schema can't fully constrain: a compilable extract
// regex, a known relation, a valid severity tier, populated selectors, and a
// transaction scope (correlation is only meaningful per transaction).
func ValidateServiceGuardrail(g apitypes.ServiceGuardrail) error {
	for _, r := range g.Rules {
		if r.Key != string(risk.RuleAttributeCorrelation) {
			continue
		}
		if len(r.Correlations) > 0 && repo.NormalizePolicyScope(repo.PolicyScope(g.Scope)) != repo.ScopeTransaction {
			return fmt.Errorf("attribute_correlation is transaction-scoped; set scope to transaction")
		}
		for i, spec := range r.Correlations {
			if err := validateCorrelationSpec(spec); err != nil {
				return fmt.Errorf("correlation %d (%q): %w", i, spec.Name, err)
			}
		}
	}
	return nil
}

func validateCorrelationSpec(spec apitypes.CorrelationSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch spec.Relation {
	case apitypes.Equals, apitypes.NotEquals:
	default:
		return fmt.Errorf("unknown relation %q", spec.Relation)
	}
	if spec.Severity != "" && !spec.Severity.Valid() {
		return fmt.Errorf("invalid severity %q", spec.Severity)
	}
	if err := validateCorrelationSelector("left", spec.Left); err != nil {
		return err
	}
	if err := validateCorrelationSelector("right", spec.Right); err != nil {
		return err
	}
	return nil
}

func validateCorrelationSelector(side string, sel apitypes.CorrelationSelector) error {
	if sel.Service == "" || sel.Span == "" || sel.Attr == "" {
		return fmt.Errorf("%s selector needs service, span and attr", side)
	}
	if sel.Extract != "" {
		if _, err := regexp.Compile(sel.Extract); err != nil {
			return fmt.Errorf("%s selector extract regex: %w", side, err)
		}
	}
	return nil
}
