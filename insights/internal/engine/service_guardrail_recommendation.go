package engine

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// A service guardrail recommendation ("lock it down") is one card per service
// whose every transaction has finished learning. It offers to enforce the
// allowlist rules the service does not enforce yet, each pre-filled from the
// learned profile, so an operator gets the full segmentation posture in one
// click - and can leave a rule out (say, egress) before applying. The card
// only ever extends the guardrail: a rule that is already enforced, whether by
// an operator or by the automatic transaction guardrail, or that the operator
// switched off, is not on it.

// serviceGuardrailRules is the card's rule order.
var serviceGuardrailRules = []repo.RuleKey{
	risk.RuleAllowedCallers,
	risk.RuleAllowedCallees,
	risk.RuleAllowedEgress,
	risk.RuleAllowedTransactions,
}

// refreshServiceGuardrailRecommendation rebuilds one service's card from its
// current guardrail and learned profile. An applied or dismissed card is the
// operator's decision and is left untouched. Returns whether a row was written.
func (e *Engine) refreshServiceGuardrailRecommendation(ctx context.Context, namespace, service string) (bool, error) {
	id := repo.ServiceGuardrailRecommendationID(namespace, service)
	existing, found, err := e.repo.GetRecommendation(ctx, id)
	if err != nil {
		return false, err
	}
	if found && existing.State != repo.RecommendationOpen {
		return false, nil
	}
	rec, ok, err := e.buildServiceGuardrailRecommendation(ctx, namespace, service)
	if err != nil {
		return false, err
	}
	now := e.clock()
	if !ok {
		// Nothing left to offer: every rule is enforced or off, or the service
		// went back to learning. Keep the row for provenance, hide it.
		if !found || existing.Stale {
			return false, nil
		}
		existing.Stale = true
		return true, e.repo.UpsertRecommendations(ctx, []repo.Recommendation{existing})
	}
	if found {
		if !existing.Stale && sameRecommendationRules(existing.Rules, rec.Rules) {
			return false, nil
		}
		rec.CreatedAt = existing.CreatedAt
	} else {
		rec.CreatedAt = now
	}
	return true, e.repo.UpsertRecommendations(ctx, []repo.Recommendation{rec})
}

// buildServiceGuardrailRecommendation composes the card: ok is false when the
// service is still learning or has no rule left to enforce.
func (e *Engine) buildServiceGuardrailRecommendation(ctx context.Context, namespace, service string) (repo.Recommendation, bool, error) {
	txs, err := e.repo.ListTransactionStats(ctx, repo.TransactionListParams{Namespace: namespace, Service: service, Limit: 10000})
	if err != nil {
		return repo.Recommendation{}, false, err
	}
	if !serviceInventoryReady(txs, nil) {
		return repo.Recommendation{}, false, nil
	}
	scopeKey := repo.ServiceScopeKey(namespace, service)
	g, err := e.loadOrInitServiceGuardrail(ctx, scopeKey)
	if err != nil {
		return repo.Recommendation{}, false, err
	}
	profile, err := e.repo.GetServiceProfile(ctx, namespace, service)
	if err != nil {
		return repo.Recommendation{}, false, err
	}
	idents := make([]string, 0, len(txs))
	for _, tx := range txs {
		idents = append(idents, repo.TransactionIdentity(tx.Kind, tx.Operation))
	}
	fingerprint := e.system.IdentityFingerprint()

	var rules []repo.RecommendationRule
	for _, rule := range serviceGuardrailRules {
		if mode := repo.InventoryRuleMode(g, rule); mode == repo.RuleModeEnforce || mode == repo.RuleModeOff {
			continue
		}
		var learned []string
		switch rule {
		case risk.RuleAllowedCallers:
			learned = profile.Callers
		case risk.RuleAllowedCallees:
			learned = profile.Callees
		case risk.RuleAllowedEgress:
			learned = profile.Egress
		case risk.RuleAllowedTransactions:
			learned = idents
		}
		// A list the operator already drafted (suggest mode) is extended, never
		// replaced. Service and host names match case-insensitively at
		// enforcement time, transaction identities exactly.
		entry := repo.RecommendationRule{Rule: rule, Items: mergeAllowlist(rule != risk.RuleAllowedTransactions, g.RuleLists[rule], learned)}
		if rule == risk.RuleAllowedTransactions {
			entry.Fingerprint = fingerprint
		}
		rules = append(rules, entry)
	}
	if len(rules) == 0 {
		return repo.Recommendation{}, false, nil
	}
	return repo.Recommendation{
		ID:          repo.ServiceGuardrailRecommendationID(namespace, service),
		Kind:        repo.RecommendationKindServiceGuardrail,
		Scope:       repo.ScopeService,
		ScopeKey:    scopeKey,
		Namespace:   namespace,
		Service:     service,
		State:       repo.RecommendationOpen,
		Rules:       rules,
		SampleCount: len(txs),
		MinedAt:     e.clock(),
	}, true, nil
}

// mergeAllowlist unions allowlists, trimmed, deduplicated (case-insensitively
// when fold is set) and sorted so re-mining the same world writes the same row.
func mergeAllowlist(fold bool, lists ...[]string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, list := range lists {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			key := v
			if fold {
				key = strings.ToLower(v)
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func sameRecommendationRules(a, b []repo.RecommendationRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Rule != b[i].Rule || a[i].Fingerprint != b[i].Fingerprint || !slices.Equal(a[i].Items, b[i].Items) {
			return false
		}
	}
	return true
}

// applyServiceGuardrailRecommendation enforces the selected rules (all of the
// card when none are named) on the service's guardrail with the recommended
// lists. A rule someone enforced or switched off since the card was mined is
// skipped, so applying never overrides a decision made elsewhere.
func (e *Engine) applyServiceGuardrailRecommendation(ctx context.Context, rec repo.Recommendation, selected []repo.RuleKey) error {
	if rec.State != repo.RecommendationOpen {
		return fmt.Errorf("recommendation is %s, only open recommendations can be applied", rec.State)
	}
	if rec.Stale {
		return fmt.Errorf("recommendation is stale: the service changed since it was mined, recompute it first")
	}
	want := map[repo.RuleKey]bool{}
	if len(selected) == 0 {
		for _, r := range rec.Rules {
			want[r.Rule] = true
		}
	}
	for _, rule := range selected {
		if !slices.ContainsFunc(rec.Rules, func(r repo.RecommendationRule) bool { return r.Rule == rule }) {
			return fmt.Errorf("rule %s is not on this recommendation", rule)
		}
		want[rule] = true
	}
	g, err := e.loadOrInitServiceGuardrail(ctx, rec.ScopeKey)
	if err != nil {
		return err
	}
	g = withGuardrailMaps(g)
	if g.RuleLists == nil {
		g.RuleLists = map[repo.RuleKey][]string{}
	}
	fingerprint := e.system.IdentityFingerprint()
	var applied []repo.RuleKey
	for i := range rec.Rules {
		r := rec.Rules[i]
		if !want[r.Rule] {
			continue
		}
		if mode := repo.InventoryRuleMode(g, r.Rule); mode == repo.RuleModeEnforce || mode == repo.RuleModeOff {
			continue
		}
		if r.Rule == risk.RuleAllowedTransactions {
			if r.Fingerprint != fingerprint {
				return fmt.Errorf("the transaction identity scheme changed since this was mined, recompute the recommendation first")
			}
		}
		// Record what the rule was before overwriting it: the card's list
		// extends any allowlist the operator had drafted, so this is the only
		// way a revert can give that draft back.
		rec.Rules[i].Prior = &repo.RecommendationRulePrior{
			Items:       slices.Clone(g.RuleLists[r.Rule]),
			Mode:        g.RuleModes[r.Rule],
			Origin:      g.RuleOrigins[r.Rule],
			Fingerprint: g.RuleFingerprints[r.Rule],
		}
		if r.Rule == risk.RuleAllowedTransactions {
			repo.SetRuleFingerprint(&g, r.Rule, fingerprint)
		}
		g.RuleLists[r.Rule] = slices.Clone(r.Items)
		g.RuleModes[r.Rule] = repo.RuleModeEnforce
		g.RuleOrigins[r.Rule] = repo.GuardrailOriginRecommendation
		applied = append(applied, r.Rule)
	}
	if len(applied) == 0 {
		return fmt.Errorf("nothing to apply: the selected rules are already enforced or switched off")
	}
	if err := e.repo.UpsertServiceGuardrail(ctx, g); err != nil {
		return err
	}
	rec.State = repo.RecommendationApplied
	rec.AppliedRules = applied
	if err := e.repo.UpsertRecommendations(ctx, []repo.Recommendation{rec}); err != nil {
		return err
	}
	return e.ReloadGuardrailConfig(ctx)
}

// revertServiceGuardrailRecommendation undoes what an apply turned on and
// reopens the card. A rule the operator has since taken over (its origin is no
// longer the recommendation) is left alone entirely; on the rest only the
// entries the card contributed are withdrawn, so a list the operator drafted
// before applying, or extended after, survives.
func (e *Engine) revertServiceGuardrailRecommendation(ctx context.Context, rec repo.Recommendation) error {
	g, found, err := e.findServiceGuardrail(ctx, rec.ScopeKey)
	if err != nil {
		return err
	}
	if found {
		g = withGuardrailMaps(g)
		for _, rule := range rec.AppliedRules {
			if g.RuleOrigins[rule] != repo.GuardrailOriginRecommendation {
				continue
			}
			withdrawRecommendedRule(&g, recommendationRule(rec, rule))
		}
		if guardrailEmpty(g) {
			err = e.repo.DeleteServiceGuardrail(ctx, repo.ScopeService, g.ScopeKey)
		} else {
			err = e.repo.UpsertServiceGuardrail(ctx, g)
		}
		if err != nil {
			return err
		}
	}
	rec.State = repo.RecommendationOpen
	rec.AppliedRules = nil
	for i := range rec.Rules {
		rec.Rules[i].Prior = nil
	}
	if err := e.repo.UpsertRecommendations(ctx, []repo.Recommendation{rec}); err != nil {
		return err
	}
	// The card's lists may be out of date by now; rebuild it in the background.
	e.recommendations.requestMineService(rec.Namespace, rec.Service)
	return e.ReloadGuardrailConfig(ctx)
}

// recommendationRule returns the card's entry for one rule key, nil when the
// rule is not on the card (so nothing is known about what an apply wrote).
func recommendationRule(rec repo.Recommendation, rule repo.RuleKey) *repo.RecommendationRule {
	for i := range rec.Rules {
		if rec.Rules[i].Rule == rule {
			return &rec.Rules[i]
		}
	}
	return nil
}

// withdrawRecommendedRule takes one applied rule back off the guardrail. Only
// the entries the card contributed are removed - the operator's own, whether
// drafted before the apply or added after, stay - and the rule's previous mode,
// origin and fingerprint are restored, which leaves it observe-only when it had
// none. The rule is deleted outright only when nothing of it is left, the same
// way reverting an attribute correlation deletes the rule once its last spec is
// gone.
func withdrawRecommendedRule(g *repo.ServiceGuardrail, r *repo.RecommendationRule) {
	if r == nil {
		return
	}
	rule := r.Rule
	// Service and host names match case-insensitively, transaction identities
	// exactly - the same comparison the lists were merged under.
	contains := containsFold
	if rule == risk.RuleAllowedTransactions {
		contains = containsExact
	}
	var prior repo.RecommendationRulePrior
	if r.Prior != nil {
		prior = *r.Prior
	}
	kept := make([]string, 0, len(g.RuleLists[rule]))
	for _, item := range g.RuleLists[rule] {
		// An entry the card proposed and the rule did not already have is the
		// card's; everything else is the operator's.
		if contains(r.Items, item) && !contains(prior.Items, item) {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		// Nothing of the rule predates the apply: it goes away whole, the same
		// state the guardrail was in before.
		delete(g.RuleLists, rule)
	} else {
		g.RuleLists[rule] = kept
	}
	setOrDelete(g.RuleModes, rule, prior.Mode)
	setOrDelete(g.RuleOrigins, rule, prior.Origin)
	repo.SetRuleFingerprint(g, rule, prior.Fingerprint)
}

// setOrDelete writes a sparse map entry, removing it when the value is empty so
// "unconfigured" stays distinguishable from a written zero value.
func setOrDelete[V ~string](m map[repo.RuleKey]V, rule repo.RuleKey, value V) {
	if value == "" {
		delete(m, rule)
		return
	}
	m[rule] = value
}

func (e *Engine) findServiceGuardrail(ctx context.Context, scopeKey string) (repo.ServiceGuardrail, bool, error) {
	all, err := e.repo.ListServiceGuardrails(ctx)
	if err != nil {
		return repo.ServiceGuardrail{}, false, err
	}
	for _, g := range all {
		if repo.NormalizePolicyScope(g.Scope) == repo.ScopeService && g.ScopeKey == scopeKey {
			return g, true, nil
		}
	}
	return repo.ServiceGuardrail{}, false, nil
}

// RecomputeServiceGuardrailRecommendation asks the miner to rebuild one
// service's card now. Returns once queued; mining runs in the background.
func (e *Engine) RecomputeServiceGuardrailRecommendation(ctx context.Context, namespace, service string) error {
	txs, err := e.repo.ListTransactionStats(ctx, repo.TransactionListParams{Namespace: namespace, Service: service, Limit: 1})
	if err != nil {
		return err
	}
	if len(txs) == 0 {
		return fmt.Errorf("service %s not found", repo.ServiceScopeKey(namespace, service))
	}
	e.recommendations.requestMineService(namespace, service)
	return nil
}
