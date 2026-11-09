package engine

import (
	"encoding/json"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// This file is the risk-layer enrichment coordinator. Enrichment adds EXTRA
// signals on top of the base per-class signals so the score reflects how BAD a
// deviation is, not just that it happened. There are two tiers:
//
//   - Per-class enrichers (common): a class inspects its OWN evidence for a
//     dangerous sub-case (e.g. a new DB access that was a DELETE). These live on
//     the scorer as the RiskEnricher capability (see scorer.go).
//   - Cross-class enrichers (rare): a signal that emerges only by correlating
//     SEVERAL classes' evidence at once. These register here and receive the
//     whole evidence map.
//
// Engine.enrichRisk runs both tiers and returns their combined signals.

// enrichConfig carries the resolved scope's enricher configuration, derived
// from the alert Policy so an operator can tune enricher behavior per scope.
// Beyond signal weights (applied separately), enrichers can take per-scope
// keyword lists (e.g. sensitive tables), keyed by the enricher's list key. Zero
// value = built-in defaults everywhere.
type enrichConfig struct {
	lists map[repo.EnricherListKey][]string
}

// list returns the scope's override for a list key, or defaultList when the
// scope has none - how an enricher reads its configurable list without knowing
// whether the scope customized it.
func (c enrichConfig) list(key repo.EnricherListKey, defaultList []string) []string {
	if override, ok := c.lists[key]; ok && len(override) > 0 {
		return override
	}
	return defaultList
}

// crossClassEnricher is a risk-layer enricher that inspects the WHOLE trace's
// deviation evidence (every class at once) and emits signals a single class
// can't. Prefer a per-class RiskEnricher (scorer.go); use this only when the
// signal truly spans classes.
type crossClassEnricher func(evidence map[string]json.RawMessage, cfg enrichConfig) []risk.Signal

// crossClassEnrichers is the registry of cross-class correlation enrichers,
// consumed by enrichRisk. Intentionally empty today - register one here and it
// runs for every fired trace.
var crossClassEnrichers []crossClassEnricher

// enrichRisk runs both enrichment tiers for a fired trace and returns the extra
// signals to add on top of the base class signals: each deviated class's own
// RiskEnricher, then the cross-class registry. Called by Engine.assessRisk.
func (e *Engine) enrichRisk(deviated []repo.DeviationClass, evidence map[string]json.RawMessage, cfg enrichConfig) []risk.Signal {
	var signals []risk.Signal
	for _, class := range deviated {
		if enricher, ok := e.scorerForClass(class).(RiskEnricher); ok {
			signals = append(signals, enricher.EnrichRisk(evidence[string(class)], cfg)...)
		}
	}
	for _, enricher := range crossClassEnrichers {
		signals = append(signals, enricher(evidence, cfg)...)
	}
	return signals
}

// enricherSignal builds an enricher signal at its catalog default weight; the
// policy's per-scope override is applied later by risk.ApplyWeightOverrides.
// Enrichers stay full-confidence since the dangerous condition is binary.
// Shared by all enrichers (per-class and cross-class).
func enricherSignal(source repo.SignalSource, category risk.Category, rationale string) risk.Signal {
	weight, _ := risk.EnricherWeight(source)
	return risk.NewCategorySignal(source, category, weight, 1.0, rationale)
}
