package apimap

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// serviceRuleHighLive is how many live traces a proposed list must have been
// checked on, without a single item outside it, before it is called high
// confidence. Until then the list is only as good as the learning window.
const serviceRuleHighLive = 50

// fillServiceGuardrail adds the proposed rules with the allowlists apply would
// write, the rules a past apply turned on, and the card's presentation text.
func fillServiceGuardrail(out *apitypes.Recommendation, rec repo.Recommendation) {
	rules := make([]apitypes.RecommendationRule, 0, len(rec.Rules))
	for _, r := range rec.Rules {
		rules = append(rules, fromRecommendationRule(r))
	}
	applied := make([]string, 0, len(rec.AppliedRules))
	for _, r := range rec.AppliedRules {
		applied = append(applied, string(r))
	}
	out.Title = serviceGuardrailTitle(rec)
	out.Summary = serviceGuardrailSummary(rec)
	out.WhyItMatters = "Once the guardrail is on, a caller, dependency, external host or operation outside the learned set raises a finding on its first appearance - no baseline to fool and no learning window to hide in."
	out.Rules = rules
	out.AppliedRules = applied
	out.Confidence = serviceGuardrailConfidence(rec)
}

func fromRecommendationRule(r repo.RecommendationRule) apitypes.RecommendationRule {
	out := apitypes.RecommendationRule{
		Rule:         string(r.Rule),
		Label:        string(r.Rule),
		Items:        append([]string{}, r.Items...),
		Confidence:   serviceRuleConfidence(r),
		LiveChecked:  r.LiveChecked,
		LiveViolated: r.LiveViolated,
		LiveSince:    r.LiveSince,
		LiveLast:     r.LiveLast,
	}
	if cat, ok := risk.GuardrailRuleByKey(r.Rule); ok {
		out.Label, out.Description = cat.Label, cat.Desc
	}
	return out
}

func serviceRuleConfidence(r repo.RecommendationRule) apitypes.RecommendationConfidenceLevel {
	switch {
	case r.LiveViolated > 0:
		return apitypes.RecommendationConfidenceLevelLow
	case r.LiveChecked >= serviceRuleHighLive:
		return apitypes.RecommendationConfidenceLevelHigh
	default:
		return apitypes.RecommendationConfidenceLevelMedium
	}
}

// serviceGuardrailConfidence sums the rules' live counters and takes the
// weakest rule as the level: one contradicted list is enough to look before
// applying the whole card.
func serviceGuardrailConfidence(rec repo.Recommendation) apitypes.RecommendationConfidence {
	c := apitypes.RecommendationConfidence{
		Level:       apitypes.RecommendationConfidenceLevelHigh,
		SampleCount: rec.SampleCount,
		Reasons:     serviceGuardrailReasons(rec),
	}
	if len(rec.Rules) == 0 {
		c.Level = apitypes.RecommendationConfidenceLevelMedium
	}
	for _, r := range rec.Rules {
		c.LiveHeld += r.LiveChecked - r.LiveViolated
		c.LiveBroken += r.LiveViolated
		c.LiveSince = earliest(c.LiveSince, r.LiveSince)
		if r.LiveLast.After(c.LiveLast) {
			c.LiveLast = r.LiveLast
		}
		switch serviceRuleConfidence(r) {
		case apitypes.RecommendationConfidenceLevelLow:
			c.Level = apitypes.RecommendationConfidenceLevelLow
		case apitypes.RecommendationConfidenceLevelMedium:
			if c.Level == apitypes.RecommendationConfidenceLevelHigh {
				c.Level = apitypes.RecommendationConfidenceLevelMedium
			}
		}
	}
	return c
}

func earliest(a, b time.Time) time.Time {
	if b.IsZero() || (!a.IsZero() && a.Before(b)) {
		return a
	}
	return b
}

// The recommendation reads as one guardrail for the service; the allowlist
// categories are what it covers, and edit & apply can leave some out.
func serviceGuardrailTitle(rec repo.Recommendation) string {
	if rec.State == repo.RecommendationApplied {
		return fmt.Sprintf("%s locked down", rec.Service)
	}
	return fmt.Sprintf("Lock down %s", rec.Service)
}

// One call to action. An empty list fires on any new item; a filled list fires
// only outside what was already observed. Applied cards drop the "Apply to".
func serviceGuardrailSummary(rec repo.Recommendation) string {
	clause := serviceGuardrailCoverage(rec, rec.State == repo.RecommendationApplied)
	if rec.State == repo.RecommendationApplied {
		if clause == "" {
			return "Guardrail is on."
		}
		return "Raises a finding for " + clause + "."
	}
	if clause == "" {
		return "Apply to raise a finding."
	}
	return "Apply to raise a finding for " + clause + "."
}

// Categories the guardrail enforces, in rule order. appliedOnly keeps the ones
// a past apply turned on.
func serviceGuardrailCoverage(rec repo.Recommendation, appliedOnly bool) string {
	parts := make([]string, 0, len(rec.Rules))
	for _, r := range rec.Rules {
		if appliedOnly && !slices.Contains(rec.AppliedRules, r.Rule) {
			continue
		}
		parts = append(parts, serviceGuardrailClause(r))
	}
	return joinOr(parts)
}

func serviceGuardrailClause(r repo.RecommendationRule) string {
	thing := serviceGuardrailThing(r.Rule)
	if len(r.Items) == 0 {
		return "any " + thing
	}
	return fmt.Sprintf("any %s outside the %d observed", thing, len(r.Items))
}

func serviceGuardrailThing(key repo.RuleKey) string {
	switch key {
	case risk.RuleAllowedCallers:
		return "caller"
	case risk.RuleAllowedCallees:
		return "callee"
	case risk.RuleAllowedEgress:
		return "external host"
	case risk.RuleAllowedTransactions:
		return "transaction"
	default:
		return strings.ToLower(ruleLabel(key))
	}
}

func joinOr(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", or " + parts[len(parts)-1]
	}
}

func serviceGuardrailReasons(rec repo.Recommendation) []string {
	reasons := []string{fmt.Sprintf("Lists come from the learned profile of %d promoted transaction%s", rec.SampleCount, pluralS(rec.SampleCount))}
	var checked, violated int64
	for _, r := range rec.Rules {
		checked += r.LiveChecked
		violated += r.LiveViolated
		if r.LiveViolated > 0 {
			reasons = append(reasons, fmt.Sprintf("%s: %d of %d live traces reached something outside the list", ruleLabel(r.Rule), r.LiveViolated, r.LiveChecked))
		}
	}
	switch {
	case checked == 0:
		reasons = append(reasons, "Not yet checked against live traffic")
	case violated == 0:
		reasons = append(reasons, fmt.Sprintf("Checked on %d live traces, none outside the lists", checked))
	}
	return reasons
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ToRecommendationRuleKeys maps an apply item's rule subset.
func ToRecommendationRuleKeys(in []string) []repo.RuleKey {
	out := make([]repo.RuleKey, 0, len(in))
	for _, r := range in {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, repo.RuleKey(r))
		}
	}
	return out
}
