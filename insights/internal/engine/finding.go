package engine

import (
	"encoding/json"
	"sync"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// This file defines the render-ready "finding" types shared by every class and
// the dispatcher that maps a class to its explanation. The per-class explainer
// logic lives on each scorer (see the Explain method in scorer_dN.go) so that
// the code producing an evidence blob and the code reading it back sit together.

// FindingKind tells a UI how to present a finding. Structural findings are
// best shown as a trace-vs-trace diff (the anomaly did something extra/new in
// its span tree); metric findings are a single number compared to a learned
// threshold and are better shown as a banner/gauge.
type FindingKind string

const (
	FindingStructural FindingKind = "structural"
	FindingMetric     FindingKind = "metric"
)

// SpanHighlight marks a span the UI should call out, with a severity and a
// short human reason. SpanID matches SpanView.SpanID.
type SpanHighlight struct {
	SpanID   string `json:"spanId"`
	Severity string `json:"severity"` // high | medium | low
	Reason   string `json:"reason"`
}

// AttrHighlight points at a specific attribute on a span worth surfacing
// (e.g. the offending db.statement).
type AttrHighlight struct {
	SpanID string `json:"spanId"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// MetricComparison is the payload for a metric finding: an observed value vs
// the learned baseline threshold, in a named unit.
type MetricComparison struct {
	Observed int64  `json:"observed"`
	Baseline int64  `json:"baseline"`
	Unit     string `json:"unit"` // "us" | "bytes"
}

// Finding is the server's normalised, render-ready explanation of one
// deviation class within an anomaly. The UI never parses per-class evidence
// JSON: it renders the Title/Summary, draws the SpanHighlights/AttrHighlights
// the server computed, and switches layout on Kind. RawEvidence is included so
// a client can always fall back to the original blob.
type Finding struct {
	Class          repo.DeviationClass `json:"class"`
	Kind           FindingKind         `json:"kind"`
	Title          string              `json:"title"`
	Summary        string              `json:"summary"`
	SpanHighlights []SpanHighlight     `json:"spanHighlights,omitempty"`
	AttrHighlights []AttrHighlight     `json:"attrHighlights,omitempty"`
	Metric         *MetricComparison   `json:"metric,omitempty"`
	RawEvidence    json.RawMessage     `json:"rawEvidence,omitempty"`
}

// Explain turns one class's evidence blob (from Anomaly.Evidence[class]) into a
// render-ready Finding, using the anomaly trace's spans to attribute highlights
// back to concrete spans. Dispatch is driven by the scorer registry: a class
// whose scorer implements Explainer gets its rich finding; every other class
// falls back to a minimal finding (title + raw evidence, no highlights).
//
// Because both scoring and explaining resolve through the same registry, adding
// a class is a single change in one place - there is no parallel switch here to
// keep in sync.
func Explain(class repo.DeviationClass, evidence json.RawMessage, spans []SpanView) Finding {
	if ex, ok := explainerByClass()[class]; ok {
		return ex.Explain(evidence, spans)
	}
	return minimalFinding(class, evidence)
}

// explainerByClass indexes the registry's Explainer-capable scorers by class.
// Built once on first use; the registry is fixed for the process lifetime.
var explainerByClass = sync.OnceValue(func() map[repo.DeviationClass]Explainer {
	m := map[repo.DeviationClass]Explainer{}
	for _, s := range defaultScorers() {
		if ex, ok := s.(Explainer); ok {
			m[s.Class()] = ex
		}
	}
	return m
})

// minimalFinding is the fallback for a class whose scorer has no Explainer: a
// title and the raw evidence for client-side inspection, no highlights.
func minimalFinding(class repo.DeviationClass, evidence json.RawMessage) Finding {
	kind := FindingStructural
	if isAdditiveClass(class) {
		kind = FindingMetric
	}
	return Finding{
		Class:       class,
		Kind:        kind,
		Title:       titleFor(class),
		Summary:     "Deviation detected. See evidence for details.",
		RawEvidence: evidence,
	}
}

// titleFor is the human label for a class, sourced from the risk catalog (the
// single source of truth), falling back to the raw class id.
func titleFor(class repo.DeviationClass) string {
	return risk.ClassLabel(class)
}
