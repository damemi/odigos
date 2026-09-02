package apimap

import (
	"fmt"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// severityFor is the wire severity tier ("high", "critical", ...) for a score.
func severityFor(score int) apitypes.Severity {
	return apitypes.Severity(risk.SeverityForScore(score))
}

// ruleLabel is the human label for a guardrail rule key, or the raw key.
func ruleLabel(key repo.RuleKey) string {
	if rule, ok := risk.GuardrailRuleByKey(key); ok {
		return rule.Label
	}
	return string(key)
}

// FromAnomalyIssueSummary maps one repo.AnomalyIssue to the list wire type
// (no evidence). Severity is derived from max_score.
func FromAnomalyIssueSummary(a repo.AnomalyIssue) apitypes.AnomalyIssueSummary {
	classes := make([]apitypes.DeviationClass, len(a.TriggeredClasses))
	for i, c := range a.TriggeredClasses {
		classes[i] = apitypes.DeviationClass(c)
	}
	name, dims := wireOperationIdentity(a.Operation)
	return apitypes.AnomalyIssueSummary{
		TransactionId:      a.TransactionID,
		Signature:          a.Signature,
		Service:            a.Service,
		Namespace:          a.Namespace,
		Operation:          a.Operation,
		OperationName:      name,
		IdentityDimensions: dims,
		Kind:               apitypes.TransactionKind(a.Kind),
		TriggeredClasses:   classes,
		Offending:          a.Offending,
		PolicyId:           a.PolicyID,
		Occurrences:        a.Occurrences,
		MaxScore:           a.MaxScore,
		Severity:           severityFor(a.MaxScore),
		FirstSeen:          a.FirstSeen,
		LastSeen:           a.LastSeen,
		LastTraceId:        a.LastTraceID,
		Status:             apitypes.AnomalyStatus(a.Status),
	}
}

// FromAnomalyIssue maps one repo.AnomalyIssue to the detail wire type, including
// evidence. Risk, class_findings, and compare traces are attached by
// GetAnomalyIssueDetail (investigate enrichment).
func FromAnomalyIssue(anomaly repo.AnomalyIssue) apitypes.AnomalyIssue {
	classes := make([]apitypes.DeviationClass, len(anomaly.TriggeredClasses))
	for i, c := range anomaly.TriggeredClasses {
		classes[i] = apitypes.DeviationClass(c)
	}
	name, dimensions := wireOperationIdentity(anomaly.Operation)
	return apitypes.AnomalyIssue{
		TransactionId:      anomaly.TransactionID,
		Signature:          anomaly.Signature,
		Service:            anomaly.Service,
		Namespace:          anomaly.Namespace,
		Operation:          anomaly.Operation,
		OperationName:      name,
		IdentityDimensions: dimensions,
		Kind:               apitypes.TransactionKind(anomaly.Kind),
		TriggeredClasses:   classes,
		Offending:          anomaly.Offending,
		Evidence:           anomaly.Evidence,
		PolicyId:           anomaly.PolicyID,
		Occurrences:        anomaly.Occurrences,
		MaxScore:           anomaly.MaxScore,
		Severity:           severityFor(anomaly.MaxScore),
		FirstSeen:          anomaly.FirstSeen,
		LastSeen:           anomaly.LastSeen,
		LastTraceId:        anomaly.LastTraceID,
		Status:             apitypes.AnomalyStatus(anomaly.Status),
		ClassFindings:      []apitypes.AnomalyClassFinding{},
	}
}

// AnomalyIssueList wraps repo.AnomalyIssue rows as AnomalyIssueSummary items.
func AnomalyIssueList(in []repo.AnomalyIssue) apitypes.AnomalyIssueList {
	items := make([]apitypes.AnomalyIssueSummary, len(in))
	for i, a := range in {
		items[i] = FromAnomalyIssueSummary(a)
	}
	return apitypes.AnomalyIssueList{Count: len(items), Items: items}
}

// FromGuardrailViolation maps one repo.GuardrailViolationGroup to its wire type.
func FromGuardrailViolation(g repo.GuardrailViolationGroup) apitypes.GuardrailViolation {
	return apitypes.GuardrailViolation{
		Service:     g.Service,
		Namespace:   g.Namespace,
		ScopeKey:    g.ScopeKey,
		RuleKey:     string(g.RuleKey),
		RuleLabel:   ruleLabel(g.RuleKey),
		Offending:   g.Offending,
		Occurrences: g.Occurrences,
		MaxScore:    g.MaxScore,
		Severity:    severityFor(g.MaxScore),
		FirstSeen:   g.FirstSeen,
		LastSeen:    g.LastSeen,
		Status:      apitypes.ViolationStatus(g.Status),
	}
}

// GuardrailViolationList wraps repo violation groups in the wire list envelope.
func GuardrailViolationList(in []repo.GuardrailViolationGroup) apitypes.GuardrailViolationList {
	items := make([]apitypes.GuardrailViolation, len(in))
	for i, g := range in {
		items[i] = FromGuardrailViolation(g)
	}
	return apitypes.GuardrailViolationList{Count: len(items), Items: items}
}

// FromGuardrailViolationDetail maps a group to the investigate wire type (list
// fields plus summary). Span highlights and evidence_trace are attached by
// GetGuardrailViolationDetail.
func FromGuardrailViolationDetail(g repo.GuardrailViolationGroup) apitypes.GuardrailViolationDetail {
	v := FromGuardrailViolation(g)
	return apitypes.GuardrailViolationDetail{
		Service:     v.Service,
		Namespace:   v.Namespace,
		ScopeKey:    v.ScopeKey,
		RuleKey:     v.RuleKey,
		RuleLabel:   v.RuleLabel,
		Offending:   v.Offending,
		Occurrences: v.Occurrences,
		MaxScore:    v.MaxScore,
		Severity:    v.Severity,
		FirstSeen:   v.FirstSeen,
		LastSeen:    v.LastSeen,
		Status:      v.Status,
		Summary:     violationSummary(v.Service, v.RuleLabel, v.Offending),
	}
}

// violationSummary is the render-ready investigate sentence. The UI should
// display it as-is (same pattern as anomaly class_findings.summary).
func violationSummary(service, ruleLabel, offending string) string {
	return fmt.Sprintf("%s broke %s on %s. This is one captured trace as evidence — the highlighted span(s) show the forbidden edge.",
		service, ruleLabel, offending)
}

// FromServiceGuardrail maps one repo.ServiceGuardrail to its wire type, walking
// the rule catalog so output order is stable and every rule carries its label.
func FromServiceGuardrail(g repo.ServiceGuardrail) apitypes.ServiceGuardrail {
	var rules []apitypes.GuardrailRuleConfig
	for _, rule := range risk.GuardrailRules {
		list, hasList := g.RuleLists[rule.Key]
		mode, hasMode := g.RuleModes[rule.Key]
		if !hasList && !hasMode {
			continue
		}
		rules = append(rules, apitypes.GuardrailRuleConfig{
			Key:       string(rule.Key),
			Label:     rule.Label,
			Mode:      apitypes.RuleMode(repo.NormalizeRuleMode(mode)),
			Allowlist: list,
			Origin:    g.RuleOrigins[rule.Key],
		})
	}
	return apitypes.ServiceGuardrail{
		Scope:    apitypes.PolicyScope(repo.NormalizePolicyScope(g.Scope)),
		ScopeKey: g.ScopeKey,
		Rules:    rules,
	}
}

// ServiceGuardrailList wraps repo guardrails in the wire list envelope.
func ServiceGuardrailList(in []repo.ServiceGuardrail) apitypes.ServiceGuardrailList {
	items := make([]apitypes.ServiceGuardrail, len(in))
	for i, g := range in {
		items[i] = FromServiceGuardrail(g)
	}
	return apitypes.ServiceGuardrailList{Count: len(items), Items: items}
}

// FromLearningPolicy maps one repo.LearningPolicy to its wire type.
func FromLearningPolicy(p repo.LearningPolicy) apitypes.LearningPolicy {
	conditions := make([]apitypes.LearningCondition, len(p.Conditions))
	for i, c := range p.Conditions {
		conditions[i] = apitypes.LearningCondition{
			Type:               apitypes.LearningConditionType(c.Type),
			MinObservations:    c.MinObservations,
			MinDurationMinutes: c.MinDurationMinutes,
			StableObservations: c.StableObservations,
			StableMinutes:      c.StableMinutes,
		}
	}
	return apitypes.LearningPolicy{
		Class:      apitypes.DeviationClass(p.Class),
		Mode:       apitypes.LearningMode(p.Mode),
		Conditions: conditions,
		MinMatches: p.MinMatches,
		Scope:      apitypes.PolicyScope(repo.NormalizePolicyScope(p.Scope)),
		ScopeKey:   p.ScopeKey,
	}
}

// LearningPolicyList wraps repo learning policies in the wire list envelope.
func LearningPolicyList(in []repo.LearningPolicy) apitypes.LearningPolicyList {
	items := make([]apitypes.LearningPolicy, len(in))
	for i, p := range in {
		items[i] = FromLearningPolicy(p)
	}
	return apitypes.LearningPolicyList{Count: len(items), Items: items}
}

// FindingFromAnomaly flattens an anomaly issue into a unified Finding row.
func FindingFromAnomaly(a repo.AnomalyIssue) apitypes.Finding {
	classes := make([]apitypes.DeviationClass, len(a.TriggeredClasses))
	for i, class := range a.TriggeredClasses {
		classes[i] = apitypes.DeviationClass(class)
	}
	name, dims := wireOperationIdentity(a.Operation)
	return apitypes.Finding{
		Kind:               apitypes.Anomaly,
		Service:            a.Service,
		Namespace:          a.Namespace,
		Title:              name,
		Operation:          a.Operation,
		OperationName:      name,
		IdentityDimensions: dims,
		Offending:          a.Offending,
		Score:              a.MaxScore,
		Severity:           severityFor(a.MaxScore),
		Occurrences:        a.Occurrences,
		FirstSeen:          a.FirstSeen,
		LastSeen:           a.LastSeen,
		Status:             string(a.Status),
		TransactionId:      a.TransactionID,
		Signature:          a.Signature,
		TriggeredClasses:   classes,
	}
}

// FindingFromViolation flattens a guardrail violation into a unified Finding row.
func FindingFromViolation(g repo.GuardrailViolationGroup) apitypes.Finding {
	return apitypes.Finding{
		Kind:        apitypes.Violation,
		Service:     g.Service,
		Namespace:   g.Namespace,
		Title:       ruleLabel(g.RuleKey),
		Offending:   g.Offending,
		Score:       g.MaxScore,
		Severity:    severityFor(g.MaxScore),
		Occurrences: g.Occurrences,
		FirstSeen:   g.FirstSeen,
		LastSeen:    g.LastSeen,
		Status:      string(g.Status),
		ScopeKey:    g.ScopeKey,
		RuleKey:     string(g.RuleKey),
	}
}

// FindingList wraps already-built Finding rows in the wire list envelope.
func FindingList(items []apitypes.Finding) apitypes.FindingList {
	return apitypes.FindingList{Count: len(items), Items: items}
}
