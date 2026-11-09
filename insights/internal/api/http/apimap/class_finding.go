package apimap

import (
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/engine"
)

// FromClassFinding maps an engine Finding to the investigate wire type.
func FromClassFinding(f engine.Finding) apitypes.AnomalyClassFinding {
	out := apitypes.AnomalyClassFinding{
		Class:       apitypes.DeviationClass(f.Class),
		Kind:        apitypes.AnomalyClassFindingKind(f.Kind),
		Title:       f.Title,
		Summary:     f.Summary,
		RawEvidence: f.RawEvidence,
	}
	if len(f.SpanHighlights) > 0 {
		out.SpanHighlights = make([]apitypes.AnomalySpanHighlight, len(f.SpanHighlights))
		for i, h := range f.SpanHighlights {
			out.SpanHighlights[i] = apitypes.AnomalySpanHighlight{
				SpanId:   h.SpanID,
				Severity: h.Severity,
				Reason:   h.Reason,
			}
		}
	}
	if len(f.AttrHighlights) > 0 {
		out.AttrHighlights = make([]apitypes.AnomalyAttrHighlight, len(f.AttrHighlights))
		for i, h := range f.AttrHighlights {
			out.AttrHighlights[i] = apitypes.AnomalyAttrHighlight{
				SpanId: h.SpanID,
				Key:    h.Key,
				Value:  h.Value,
			}
		}
	}
	if f.Metric != nil {
		out.Metric = &apitypes.AnomalyMetricComparison{
			Observed: f.Metric.Observed,
			Baseline: f.Metric.Baseline,
			Unit:     f.Metric.Unit,
		}
	}
	return out
}

// FromSpanHighlights maps engine span highlights to the investigate wire type.
func FromSpanHighlights(in []engine.SpanHighlight) []apitypes.AnomalySpanHighlight {
	if len(in) == 0 {
		return nil
	}
	out := make([]apitypes.AnomalySpanHighlight, len(in))
	for i, h := range in {
		out[i] = apitypes.AnomalySpanHighlight{
			SpanId:   h.SpanID,
			Severity: h.Severity,
			Reason:   h.Reason,
		}
	}
	return out
}
