package apimap

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Confidence thresholds. A recommendation is only ever produced above the
// configured hold ratio, so "level" mostly reflects how much evidence backs
// it and whether live traffic has contradicted it.
const (
	// confidenceHighDistinct is how many distinct values a relation must have
	// held across before sample evidence alone is called high confidence.
	confidenceHighDistinct = 10
	// confidenceHighLive is how many live traces without a break count as high.
	confidenceHighLive = 50
)

// FromRecommendation maps one engine view to its wire type, adding the
// presentation fields (title, summary, confidence level and reasons) the UI
// shows without having to know what the numbers mean.
func FromRecommendation(v engine.RecommendationView) apitypes.Recommendation {
	rec := v.Recommendation
	spec := fromCorrelationSpecs([]repo.CorrelationSpec{rec.Spec})[0]
	return apitypes.Recommendation{
		Id:              rec.ID,
		Kind:            apitypes.RecommendationKind(rec.Kind),
		State:           apitypes.RecommendationState(rec.State),
		Stale:           rec.Stale,
		Rank:            rec.Rank,
		Scope:           apitypes.PolicyScope(rec.Scope),
		ScopeKey:        rec.ScopeKey,
		TransactionId:   rec.TransactionID,
		TransactionKind: apitypes.TransactionKind(rec.TxKind),
		Service:         rec.Service,
		Namespace:       rec.Namespace,
		Operation:       rec.Operation,
		Title:           recommendationTitle(rec),
		Summary:         recommendationSummary(rec),
		Spec:            spec,
		Transform:       rec.Transform,
		Transport:       rec.Transport,
		Confidence:      recommendationConfidence(rec),
		Examples:        fromRecommendationExamples(rec.Examples),
		AlreadyCovered:  v.AlreadyCovered,
		MinedAt:         rec.MinedAt,
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
	}
}

// RecommendationList wraps views in the wire list envelope.
func RecommendationList(in []engine.RecommendationView) apitypes.RecommendationList {
	items := make([]apitypes.Recommendation, len(in))
	for i, v := range in {
		items[i] = FromRecommendation(v)
	}
	return apitypes.RecommendationList{Count: len(items), Items: items}
}

func fromRecommendationExamples(in []repo.RecommendationExample) []apitypes.RecommendationExample {
	out := make([]apitypes.RecommendationExample, len(in))
	for i, ex := range in {
		out[i] = apitypes.RecommendationExample{
			TraceId:    ex.TraceID,
			LeftValue:  ex.LeftValue,
			RightValue: ex.RightValue,
			ObservedAt: ex.ObservedAt,
		}
	}
	return out
}

// recommendationTitle is the one-line headline: the two attribute positions,
// with services spelled out when the relation crosses a service boundary.
func recommendationTitle(rec repo.Recommendation) string {
	l, r := rec.Spec.Left, rec.Spec.Right
	if strings.EqualFold(l.Service, r.Service) {
		return fmt.Sprintf("%s %s matches %s %s", l.Span, l.Attr, r.Span, r.Attr)
	}
	return fmt.Sprintf("%s/%s %s matches %s/%s %s", l.Service, l.Span, l.Attr, r.Service, r.Span, r.Attr)
}

// recommendationSummary explains the relation and its evidence in one sentence.
func recommendationSummary(rec repo.Recommendation) string {
	how := "carried the same value as"
	switch rec.Transform {
	case "core":
		how = "matched, once a constant prefix/suffix is removed,"
	case "digits":
		how = "carried the same numeric id as"
	case "contains":
		how = "embedded the value of"
	}
	l, r := rec.Spec.Left, rec.Spec.Right
	s := fmt.Sprintf("In %d of %d sampled traces (%d distinct values), %s on %s (%s) %s %s on %s (%s).",
		rec.Held, rec.Observed, rec.Distinct, l.Attr, l.Span, l.Service, how, r.Attr, r.Span, r.Service)
	if rec.Transport {
		s += " The transport copies this field between the two ends, so a break means tampering or misattribution in flight, not a business-logic bypass."
	}
	return s
}

func recommendationConfidence(rec repo.Recommendation) apitypes.RecommendationConfidence {
	c := apitypes.RecommendationConfidence{
		Observed:    rec.Observed,
		Held:        rec.Held,
		Distinct:    rec.Distinct,
		SampleCount: rec.SampleCount,
		LiveHeld:    rec.LiveHeld,
		LiveBroken:  rec.LiveBroken,
		LiveSince:   rec.LiveSince,
		LiveLast:    rec.LiveLast,
	}
	if rec.Observed > 0 {
		c.HoldRatio = float64(rec.Held) / float64(rec.Observed)
	}
	c.Level = confidenceLevel(rec)
	c.Reasons = confidenceReasons(rec)
	return c
}

func confidenceLevel(rec repo.Recommendation) apitypes.RecommendationConfidenceLevel {
	switch {
	case rec.LiveBroken > 0:
		return apitypes.RecommendationConfidenceLevelLow
	case rec.Held == rec.Observed && (rec.Distinct >= confidenceHighDistinct || rec.LiveHeld >= confidenceHighLive):
		return apitypes.RecommendationConfidenceLevelHigh
	default:
		return apitypes.RecommendationConfidenceLevelMedium
	}
}

func confidenceReasons(rec repo.Recommendation) []string {
	reasons := []string{
		fmt.Sprintf("Held on %d of %d stored samples across %d distinct values", rec.Held, rec.Observed, rec.Distinct),
	}
	switch {
	case rec.LiveBroken > 0:
		reasons = append(reasons, fmt.Sprintf("Broke on %d of %d live traces since %s", rec.LiveBroken, rec.LiveHeld+rec.LiveBroken, rec.LiveSince.Format("Jan 2 15:04")))
	case rec.LiveHeld > 0:
		reasons = append(reasons, fmt.Sprintf("Held on all %d live traces since %s", rec.LiveHeld, rec.LiveSince.Format("Jan 2 15:04")))
	default:
		reasons = append(reasons, "Not yet checked against live traffic")
	}
	switch rec.Transform {
	case "exact":
		reasons = append(reasons, "Values are identical")
	case "core":
		reasons = append(reasons, "Values are identical after removing a constant prefix/suffix")
	case "digits":
		reasons = append(reasons, "Values share the same numeric id")
	case "contains":
		reasons = append(reasons, "One value is embedded in the other")
	}
	if !strings.EqualFold(rec.Spec.Left.Service, rec.Spec.Right.Service) {
		reasons = append(reasons, "Crosses a service boundary")
	}
	return reasons
}

// FromRecommendationBulkResult maps a bulk action outcome to the wire type.
func FromRecommendationBulkResult(r engine.RecommendationBulkResult) apitypes.RecommendationBulkResult {
	errs := make([]apitypes.RecommendationItemError, len(r.Errors))
	for i, e := range r.Errors {
		errs[i] = apitypes.RecommendationItemError{Id: e.ID, Error: e.Err.Error()}
	}
	return apitypes.RecommendationBulkResult{Done: r.Done, Failed: len(errs), Errors: errs}
}

// ToRecommendationApplyItems maps the apply request body.
func ToRecommendationApplyItems(req apitypes.RecommendationApplyRequest) []engine.RecommendationApplyItem {
	items := make([]engine.RecommendationApplyItem, len(req.Items))
	for i, it := range req.Items {
		items[i] = engine.RecommendationApplyItem{ID: strings.TrimSpace(it.Id)}
		if it.Spec != nil {
			spec := toCorrelationSpecs([]apitypes.CorrelationSpec{*it.Spec})[0]
			items[i].Spec = &spec
		}
	}
	return items
}

// ToCorrelationSpec maps one wire spec to its repo shape.
func ToCorrelationSpec(spec apitypes.CorrelationSpec) repo.CorrelationSpec {
	return toCorrelationSpecs([]apitypes.CorrelationSpec{spec})[0]
}

// FromRecommendationPreview maps a preview to the wire type.
func FromRecommendationPreview(p engine.RecommendationPreview) apitypes.RecommendationPreview {
	out := apitypes.RecommendationPreview{
		SampleCount:     p.SampleCount,
		Observed:        p.Observed,
		Held:            p.Held,
		Distinct:        p.Distinct,
		Examples:        fromRecommendationExamples(p.Examples),
		CounterExamples: fromRecommendationExamples(p.CounterExamples),
	}
	if p.Observed > 0 {
		out.HoldRatio = float64(p.Held) / float64(p.Observed)
	}
	return out
}
