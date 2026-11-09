package notify

import (
	"strings"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// severityRank orders tiers for the min_severity filter.
var severityRank = map[risk.Severity]int{
	risk.SeverityInfo:     0,
	risk.SeverityLow:      1,
	risk.SeverityMedium:   2,
	risk.SeverityHigh:     3,
	risk.SeverityCritical: 4,
}

// EventFromAnomaly maps an anomaly issue to the backend-agnostic Event using
// the same titles and summaries the product API renders, so a Slack message
// and the findings feed read the same.
func EventFromAnomaly(a repo.AnomalyIssue, action repo.DispatchAction) Event {
	classes := make([]string, len(a.TriggeredClasses))
	for i, c := range a.TriggeredClasses {
		classes[i] = string(c)
	}
	title, _ := repo.SplitOperationIdentity(a.Operation)
	return Event{
		Key:           repo.AnomalyFindingKey(a.TransactionID, a.Signature),
		Kind:          KindAnomaly,
		Action:        action,
		Service:       a.Service,
		Namespace:     a.Namespace,
		Title:         title,
		Summary:       risk.AnomalySummary(a.TriggeredClasses),
		Offending:     a.Offending,
		Score:         a.MaxScore,
		Severity:      risk.SeverityForScore(a.MaxScore),
		Status:        string(a.Status),
		Occurrences:   a.Occurrences,
		FirstSeen:     a.FirstSeen,
		LastSeen:      a.LastSeen,
		LastTraceID:   a.LastTraceID,
		TransactionID: a.TransactionID,
		Signature:     a.Signature,
		PolicyID:      a.PolicyID,
		Labels: map[string]string{
			"operation":         a.Operation,
			"transaction_kind":  string(a.Kind),
			"triggered_classes": strings.Join(classes, ","),
		},
	}
}

// EventFromViolation maps a guardrail violation group to an Event.
func EventFromViolation(g repo.GuardrailViolationGroup, action repo.DispatchAction) Event {
	label := string(g.RuleKey)
	if rule, ok := risk.GuardrailRuleByKey(g.RuleKey); ok {
		label = rule.Label
	}
	return Event{
		Key:         repo.ViolationFindingKey(g.ScopeKey, g.RuleKey, g.Offending),
		Kind:        KindViolation,
		Action:      action,
		Service:     g.Service,
		Namespace:   g.Namespace,
		Title:       label,
		Summary:     risk.ViolationSummary(label, g.Offending),
		Offending:   g.Offending,
		Score:       g.MaxScore,
		Severity:    risk.SeverityForScore(g.MaxScore),
		Status:      string(g.Status),
		Occurrences: g.Occurrences,
		FirstSeen:   g.FirstSeen,
		LastSeen:    g.LastSeen,
		LastTraceID: g.LastTraceID,
		ScopeKey:    g.ScopeKey,
		RuleKey:     string(g.RuleKey),
		Labels:      map[string]string{"rule": string(g.RuleKey)},
	}
}

// PassesFilters applies the integration's own filters (kind allowlist and
// minimum severity) to an event. Routing is decided separately by Router.
func PassesFilters(f repo.IntegrationFilters, e Event) bool {
	if len(f.Kinds) > 0 {
		found := false
		for _, k := range f.Kinds {
			if Kind(k) == e.Kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.MinSeverity != "" {
		if severityRank[e.Severity] < severityRank[risk.Severity(f.MinSeverity)] {
			return false
		}
	}
	return true
}

// IsOpen reports whether a finding status still counts as active for
// delivery purposes.
func IsOpen(status string) bool {
	return status == "" || status == string(repo.AnomalyIssueOpen)
}
