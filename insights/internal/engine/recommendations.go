package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// Operator actions on guardrail recommendations. Every action is exposed through
// the HTTP API and the debug UI alike; nothing here touches the ingest path
// except the guardrail snapshot reload, which is the same one a manual
// guardrail edit triggers.

var (
	ErrRecommendationNotFound    = errors.New("recommendation not found")
	ErrRecommendationUnsupported = errors.New("unsupported recommendation kind")
)

// RecommendationView is a stored recommendation plus what only the engine can
// tell: whether the transaction guardrail already carries an equivalent rule.
type RecommendationView struct {
	repo.Recommendation
	// AlreadyCovered means the transaction guardrail has a rule on the same
	// two attributes (applied earlier, or written by hand). Applying again
	// would replace that rule.
	AlreadyCovered bool
}

// RecommendationApplyItem is one recommendation to apply, optionally with the
// operator's edits to the rule it creates.
type RecommendationApplyItem struct {
	ID   string
	Spec *repo.CorrelationSpec
}

// RecommendationItemError is one failed item of a bulk action.
type RecommendationItemError struct {
	ID  string
	Err error
}

// RecommendationBulkResult reports a bulk action: items that succeeded and, per
// failed id, why. A bulk action never stops at the first failure.
type RecommendationBulkResult struct {
	Done   int
	Errors []RecommendationItemError
}

// RecommendationPreview is how a spec fares on a transaction's stored relation
// samples: the same numbers the miner would report for it.
type RecommendationPreview struct {
	SampleCount     int
	Observed        int
	Held            int
	Distinct        int
	Examples        []repo.RecommendationExample
	CounterExamples []repo.RecommendationExample
}

// ListRecommendations returns recommendations matching filter with
// AlreadyCovered resolved against the live guardrail snapshot.
func (e *Engine) ListRecommendations(ctx context.Context, filter repo.RecommendationFilter) ([]RecommendationView, error) {
	recs, err := e.repo.ListRecommendations(ctx, filter)
	if err != nil {
		return nil, err
	}
	cfg := e.guardrails.config.Load()
	out := make([]RecommendationView, 0, len(recs))
	for _, rec := range recs {
		out = append(out, RecommendationView{Recommendation: rec, AlreadyCovered: e.recommendationCovered(cfg, rec)})
	}
	return out, nil
}

// GetRecommendation returns one recommendation by id.
func (e *Engine) GetRecommendation(ctx context.Context, id string) (RecommendationView, bool, error) {
	rec, ok, err := e.repo.GetRecommendation(ctx, id)
	if err != nil || !ok {
		return RecommendationView{}, ok, err
	}
	return RecommendationView{Recommendation: rec, AlreadyCovered: e.recommendationCovered(e.guardrails.config.Load(), rec)}, true, nil
}

func (e *Engine) recommendationCovered(cfg *GuardrailConfig, rec repo.Recommendation) bool {
	if cfg == nil || rec.Kind != repo.RecommendationKindAttributeCorrelation {
		return false
	}
	g, ok := cfg.ResolveTransactionGuardrail(rec.TransactionID)
	if !ok {
		return false
	}
	for _, spec := range g.RuleCorrelations[risk.RuleAttributeCorrelation] {
		if sameCorrelationSelectors(spec, rec.Spec) {
			return true
		}
	}
	return false
}

// sameCorrelationSelectors reports whether two specs assert on the same two
// attributes (either order), ignoring extract, relation, and metadata.
func sameCorrelationSelectors(a, b repo.CorrelationSpec) bool {
	same := func(x, y repo.CorrelationSelector) bool {
		return strings.EqualFold(x.Service, y.Service) && x.Span == y.Span && x.Attr == y.Attr
	}
	return (same(a.Left, b.Left) && same(a.Right, b.Right)) || (same(a.Left, b.Right) && same(a.Right, b.Left))
}

// ApplyRecommendations turns each recommendation into a rule on its
// transaction's guardrail and marks it applied. Items are independent: one
// failing does not stop the rest.
func (e *Engine) ApplyRecommendations(ctx context.Context, items []RecommendationApplyItem) RecommendationBulkResult {
	var res RecommendationBulkResult
	for _, item := range items {
		if err := e.applyRecommendation(ctx, item); err != nil {
			res.Errors = append(res.Errors, RecommendationItemError{ID: item.ID, Err: err})
			continue
		}
		res.Done++
	}
	return res
}

func (e *Engine) applyRecommendation(ctx context.Context, item RecommendationApplyItem) error {
	rec, ok, err := e.repo.GetRecommendation(ctx, item.ID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRecommendationNotFound
	}
	if rec.Kind != repo.RecommendationKindAttributeCorrelation {
		return ErrRecommendationUnsupported
	}
	spec := rec.Spec
	if item.Spec != nil {
		if !sameCorrelationSelectors(rec.Spec, *item.Spec) {
			return fmt.Errorf("edited spec must keep the recommendation's service, span and attr on both sides")
		}
		spec = *item.Spec
	}
	if err := validateCorrelationSpec(spec); err != nil {
		return err
	}
	g, err := e.loadOrInitTransactionGuardrail(ctx, rec.TransactionID)
	if err != nil {
		return err
	}
	specs := g.RuleCorrelations[risk.RuleAttributeCorrelation]
	replaced := false
	for i := range specs {
		if sameCorrelationSelectors(specs[i], spec) {
			specs[i] = spec
			replaced = true
		}
	}
	if !replaced {
		specs = append(specs, spec)
	}
	if _, had := g.RuleCorrelations[risk.RuleAttributeCorrelation]; !had {
		// Provenance is per rule, not per spec: the first spec decides it and a
		// hand-written rule keeps its manual origin when a recommendation joins it.
		g.RuleOrigins[risk.RuleAttributeCorrelation] = repo.GuardrailOriginRecommendation
	}
	g.RuleCorrelations[risk.RuleAttributeCorrelation] = specs
	g.RuleModes[risk.RuleAttributeCorrelation] = repo.RuleModeEnforce
	if err := e.repo.UpsertServiceGuardrail(ctx, g); err != nil {
		return err
	}
	if err := e.repo.SetRecommendationState(ctx, rec.ID, repo.RecommendationApplied); err != nil {
		return err
	}
	return e.ReloadGuardrailConfig(ctx)
}

// RevertRecommendations removes the rule an applied recommendation created and
// reopens it. A rule the operator edited after applying is removed as well: the
// match is on the two attributes, not on the exact spec.
func (e *Engine) RevertRecommendations(ctx context.Context, ids []string) RecommendationBulkResult {
	var res RecommendationBulkResult
	for _, id := range ids {
		if err := e.revertRecommendation(ctx, id); err != nil {
			res.Errors = append(res.Errors, RecommendationItemError{ID: id, Err: err})
			continue
		}
		res.Done++
	}
	return res
}

func (e *Engine) revertRecommendation(ctx context.Context, id string) error {
	rec, ok, err := e.repo.GetRecommendation(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRecommendationNotFound
	}
	if rec.State != repo.RecommendationApplied {
		return fmt.Errorf("recommendation is %s, only applied recommendations can be reverted", rec.State)
	}
	g, found, err := e.findTransactionGuardrail(ctx, rec.TransactionID)
	if err != nil {
		return err
	}
	if found {
		kept := g.RuleCorrelations[risk.RuleAttributeCorrelation][:0]
		for _, spec := range g.RuleCorrelations[risk.RuleAttributeCorrelation] {
			if !sameCorrelationSelectors(spec, rec.Spec) {
				kept = append(kept, spec)
			}
		}
		if len(kept) == 0 {
			delete(g.RuleCorrelations, risk.RuleAttributeCorrelation)
			delete(g.RuleModes, risk.RuleAttributeCorrelation)
			delete(g.RuleOrigins, risk.RuleAttributeCorrelation)
		} else {
			g.RuleCorrelations[risk.RuleAttributeCorrelation] = kept
		}
		if guardrailEmpty(g) {
			err = e.repo.DeleteServiceGuardrail(ctx, repo.ScopeTransaction, g.ScopeKey)
		} else {
			err = e.repo.UpsertServiceGuardrail(ctx, g)
		}
		if err != nil {
			return err
		}
	}
	if err := e.repo.SetRecommendationState(ctx, rec.ID, repo.RecommendationOpen); err != nil {
		return err
	}
	return e.ReloadGuardrailConfig(ctx)
}

// DismissRecommendations rejects recommendations. The decision is sticky: the
// miner keeps the dismissed state when it finds the same relation again.
func (e *Engine) DismissRecommendations(ctx context.Context, ids []string) RecommendationBulkResult {
	return e.setRecommendationStates(ctx, ids, repo.RecommendationDismissed, repo.RecommendationOpen)
}

// RestoreRecommendations moves dismissed recommendations back to open.
func (e *Engine) RestoreRecommendations(ctx context.Context, ids []string) RecommendationBulkResult {
	return e.setRecommendationStates(ctx, ids, repo.RecommendationOpen, repo.RecommendationDismissed)
}

func (e *Engine) setRecommendationStates(ctx context.Context, ids []string, to, from repo.RecommendationState) RecommendationBulkResult {
	var res RecommendationBulkResult
	for _, id := range ids {
		rec, ok, err := e.repo.GetRecommendation(ctx, id)
		switch {
		case err != nil:
		case !ok:
			err = ErrRecommendationNotFound
		case rec.State != from:
			err = fmt.Errorf("recommendation is %s, expected %s", rec.State, from)
		default:
			err = e.repo.SetRecommendationState(ctx, id, to)
		}
		if err != nil {
			res.Errors = append(res.Errors, RecommendationItemError{ID: id, Err: err})
			continue
		}
		res.Done++
	}
	if res.Done > 0 {
		// Open recommendations are shadow-evaluated; the set just changed.
		if err := e.ReloadGuardrailConfig(ctx); err != nil {
			e.warn("reload guardrail config after recommendation state change", err)
		}
	}
	return res
}

// PreviewRecommendation replays an arbitrary spec (typically an edited
// recommendation) on the transaction's stored relation samples so the operator
// sees the effect of an edit before applying it.
func (e *Engine) PreviewRecommendation(ctx context.Context, transactionID int64, spec repo.CorrelationSpec) (RecommendationPreview, error) {
	if err := validateCorrelationSelectors(spec); err != nil {
		return RecommendationPreview{}, err
	}
	if spec.Relation == "" {
		spec.Relation = repo.CorrelationEquals
	}
	samples, err := e.loadRelationSamples(ctx, transactionID)
	if err != nil {
		return RecommendationPreview{}, err
	}
	st := replaySpec(spec, samples)
	return RecommendationPreview{
		SampleCount:     len(samples),
		Observed:        st.observed,
		Held:            st.held,
		Distinct:        st.distinct,
		Examples:        st.examples,
		CounterExamples: st.counterExamples,
	}, nil
}

// RecomputeRecommendations asks the miner to re-mine one transaction now, or
// every promoted transaction with unmined samples when transactionID is 0. It
// returns once the request is queued; mining runs in the background.
func (e *Engine) RecomputeRecommendations(ctx context.Context, transactionID int64) error {
	if transactionID == 0 {
		e.recommendations.requestSweep()
		return nil
	}
	if _, ok, err := e.repo.GetTransactionByID(ctx, transactionID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("transaction %d not found", transactionID)
	}
	// Reset the run record so the sweep also picks the transaction up if the
	// direct request is dropped.
	if err := e.repo.RecordRecommendationMiningRun(ctx, repo.RecommendationMiningRun{TransactionID: transactionID, ClaimedAt: e.clock(), MinedAt: e.clock(), SampleCount: 0}); err != nil {
		return err
	}
	e.recommendations.requestMine(transactionID)
	return nil
}

// ---------- helpers ----------

func (e *Engine) findTransactionGuardrail(ctx context.Context, transactionID int64) (repo.ServiceGuardrail, bool, error) {
	all, err := e.repo.ListServiceGuardrails(ctx)
	if err != nil {
		return repo.ServiceGuardrail{}, false, err
	}
	key := repo.TransactionScopeKey(transactionID)
	for _, g := range all {
		if repo.NormalizePolicyScope(g.Scope) == repo.ScopeTransaction && g.ScopeKey == key {
			return withGuardrailMaps(g), true, nil
		}
	}
	return repo.ServiceGuardrail{}, false, nil
}

func (e *Engine) loadOrInitTransactionGuardrail(ctx context.Context, transactionID int64) (repo.ServiceGuardrail, error) {
	g, found, err := e.findTransactionGuardrail(ctx, transactionID)
	if err != nil {
		return repo.ServiceGuardrail{}, err
	}
	if found {
		return g, nil
	}
	return withGuardrailMaps(repo.ServiceGuardrail{Scope: repo.ScopeTransaction, ScopeKey: repo.TransactionScopeKey(transactionID)}), nil
}

func withGuardrailMaps(g repo.ServiceGuardrail) repo.ServiceGuardrail {
	if g.RuleModes == nil {
		g.RuleModes = map[repo.RuleKey]repo.RuleMode{}
	}
	if g.RuleOrigins == nil {
		g.RuleOrigins = map[repo.RuleKey]string{}
	}
	if g.RuleCorrelations == nil {
		g.RuleCorrelations = map[repo.RuleKey][]repo.CorrelationSpec{}
	}
	return g
}

func guardrailEmpty(g repo.ServiceGuardrail) bool {
	return len(g.RuleModes) == 0 && len(g.RuleLists) == 0 && len(g.RuleCorrelations) == 0 && len(g.RuleOrigins) == 0
}

func validateCorrelationSpec(spec repo.CorrelationSpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return fmt.Errorf("rule name is required")
	}
	switch spec.Relation {
	case repo.CorrelationEquals, repo.CorrelationNotEquals:
	default:
		return fmt.Errorf("unknown relation %q", spec.Relation)
	}
	return validateCorrelationSelectors(spec)
}

func validateCorrelationSelectors(spec repo.CorrelationSpec) error {
	for side, sel := range map[string]repo.CorrelationSelector{"left": spec.Left, "right": spec.Right} {
		if sel.Service == "" || sel.Span == "" || sel.Attr == "" {
			return fmt.Errorf("%s selector needs service, span and attr", side)
		}
		if sel.Extract != "" {
			if _, err := regexp.Compile(sel.Extract); err != nil {
				return fmt.Errorf("%s selector extract regex: %w", side, err)
			}
		}
	}
	return nil
}
