package apimap

import (
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// FromAssessment maps a risk.Assessment (plus the resolved fire-at threshold) to
// the wire risk breakdown. correlation_bonus is surfaced explicitly so a client
// can reconcile the total against the summed signal contributions.
func FromAssessment(a risk.Assessment, fireAt int) apitypes.RiskAssessment {
	signals := make([]apitypes.RiskSignal, len(a.Signals))
	for i, s := range a.Signals {
		signals[i] = apitypes.RiskSignal{
			Source:       string(s.Source),
			Category:     string(s.Category),
			Weight:       s.Weight,
			Confidence:   s.Confidence,
			Contribution: s.Contribution,
			Rationale:    s.Rationale,
			Mitre:        s.Mitre,
			Owasp:        s.Owasp,
		}
	}
	categories := make([]string, len(a.Categories))
	for i, cat := range a.Categories {
		categories[i] = string(cat)
	}
	bonus := 0
	if a.Correlated {
		bonus = risk.CorrelationBonus
	}
	return apitypes.RiskAssessment{
		Score:            a.Score,
		Severity:         apitypes.Severity(a.Severity),
		Correlated:       a.Correlated,
		CorrelationBonus: bonus,
		FireAtScore:      fireAt,
		Categories:       categories,
		Signals:          signals,
	}
}
