package apimap

import (
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// categoryOrder lists risk categories in a stable, severity-descending-ish order
// for the catalog output (Go maps have no order).
var categoryOrder = []risk.Category{
	risk.CategoryImpact,
	risk.CategoryInjection,
	risk.CategoryExfiltration,
	risk.CategoryAccessAnomaly,
	risk.CategorySupplyChain,
	risk.CategoryLateralMovement,
	risk.CategoryRecon,
	risk.CategoryResourceAbuse,
}

// BuildCatalog assembles the reference catalog (GET /api/v1/catalog) from the
// risk package (the single source of truth), so the endpoint always reflects the
// live risk model.
func BuildCatalog() apitypes.Catalog {
	tags := map[string]string{}
	addTag := func(code string) {
		if code == "" {
			return
		}
		if d := risk.TagDescription(code); d != "" {
			tags[code] = d
		}
	}

	classes := make([]apitypes.CatalogClass, 0, len(repo.AllDeviationClasses))
	for _, class := range repo.AllDeviationClasses {
		w, ok := risk.DefaultWeight(class)
		if !ok {
			continue
		}
		cat := risk.Catalog[class].Category
		info := risk.Categories[cat]
		addTag(info.Mitre)
		addTag(info.Owasp)
		classes = append(classes, apitypes.CatalogClass{
			Id:                   string(class),
			Label:                risk.ClassLabel(class),
			Description:          risk.ClassDesc(class),
			BaselineLabel:        risk.ClassBaselineLabel(class),
			BaselineDescription:  risk.ClassBaselineDesc(class),
			Category:             string(cat),
			CategoryLabel:        risk.CategoryLabel(cat),
			Weight:               w,
			Mitre:                info.Mitre,
			Owasp:                info.Owasp,
		})
	}

	enrichers := make([]apitypes.CatalogEnricher, 0, len(risk.EnricherSignals))
	for _, e := range risk.EnricherSignals {
		ce := apitypes.CatalogEnricher{
			Source:      string(e.Source),
			ParentClass: string(e.Parent),
			Category:    string(e.Category),
			Weight:      e.Weight,
			Label:       e.Label,
			Description: e.Desc,
		}
		if e.List != nil {
			ce.List = &apitypes.EnricherList{
				Key:     string(e.List.Key),
				Label:   e.List.Label,
				Hint:    e.List.Hint,
				Default: e.List.Default,
			}
		}
		enrichers = append(enrichers, ce)
	}

	categories := make([]apitypes.CatalogCategory, 0, len(categoryOrder))
	for _, cat := range categoryOrder {
		info, ok := risk.Categories[cat]
		if !ok {
			continue
		}
		addTag(info.Mitre)
		addTag(info.Owasp)
		categories = append(categories, apitypes.CatalogCategory{
			Id:    string(cat),
			Label: info.Label,
			Base:  info.Base,
			Mitre: info.Mitre,
			Owasp: info.Owasp,
		})
	}

	rules := make([]apitypes.CatalogGuardrailRule, 0, len(risk.GuardrailRules))
	for _, r := range risk.GuardrailRules {
		rules = append(rules, apitypes.CatalogGuardrailRule{
			Key:         string(r.Key),
			Label:       r.Label,
			Description: r.Desc,
			Category:    string(r.Category),
			Weight:      r.Weight,
			Hint:        r.Hint,
		})
	}

	return apitypes.Catalog{
		DeviationClasses: classes,
		Enrichers:        enrichers,
		Categories:       categories,
		GuardrailRules:   rules,
		SeverityBands:    severityBands(),
		CorrelationBonus: risk.CorrelationBonus,
		Tags:             tags,
	}
}

// severityBands derives each tier's inclusive lower bound by scanning scores, so
// the bands always match risk.SeverityForScore without exporting its cutoffs.
func severityBands() []apitypes.SeverityBand {
	seen := map[risk.Severity]bool{}
	var bands []apitypes.SeverityBand
	for s := 0; s <= 100; s++ {
		sev := risk.SeverityForScore(s)
		if !seen[sev] {
			seen[sev] = true
			bands = append(bands, apitypes.SeverityBand{Severity: apitypes.Severity(sev), MinScore: s})
		}
	}
	return bands
}
