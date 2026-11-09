package engine

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// InventoryRuleMode returns the normalized mode for allowed_transactions on a
// guardrail (suggest when the rule is absent).
func InventoryRuleMode(g repo.ServiceGuardrail) repo.RuleMode {
	return repo.InventoryRuleMode(g, risk.RuleAllowedTransactions)
}

// InventoryProtected reports whether allowed_transactions is enforced.
func InventoryProtected(g repo.ServiceGuardrail) bool {
	return repo.InventoryProtected(g, risk.RuleAllowedTransactions)
}

// InventoryOptedOut reports whether the operator disabled the transaction
// guardrail for this service (mode off), so auto-enable must not recreate it.
func InventoryOptedOut(g repo.ServiceGuardrail) bool {
	return repo.InventoryOptedOut(g, risk.RuleAllowedTransactions)
}

// EnableTransactionGuardrail seeds allowed_transactions from the service's
// current transaction identities and sets the rule to enforce (manual action).
// Clears any auto origin so the rule is marked operator-owned.
func (e *Engine) EnableTransactionGuardrail(ctx context.Context, namespace, service string) error {
	return e.enableTransactionGuardrail(ctx, namespace, service, "")
}

func (e *Engine) enableTransactionGuardrail(ctx context.Context, namespace, service, origin string) error {
	scopeKey := repo.ServiceScopeKey(namespace, service)
	idents, err := e.serviceTransactionIdentities(ctx, namespace, service)
	if err != nil {
		return err
	}
	if len(idents) == 0 {
		return fmt.Errorf("no transactions to seed for %s", scopeKey)
	}
	g, err := e.loadOrInitServiceGuardrail(ctx, scopeKey)
	if err != nil {
		return err
	}
	if g.RuleLists == nil {
		g.RuleLists = map[repo.RuleKey][]string{}
	}
	if g.RuleModes == nil {
		g.RuleModes = map[repo.RuleKey]repo.RuleMode{}
	}
	if g.RuleOrigins == nil {
		g.RuleOrigins = map[repo.RuleKey]string{}
	}
	g.RuleLists[risk.RuleAllowedTransactions] = idents
	g.RuleModes[risk.RuleAllowedTransactions] = repo.RuleModeEnforce
	// Stamp the identity scheme these entries were built under, so a replica
	// using a different one skips the rule instead of flagging every operation.
	repo.SetRuleFingerprint(&g, risk.RuleAllowedTransactions, e.system.IdentityFingerprint())
	if origin != "" {
		g.RuleOrigins[risk.RuleAllowedTransactions] = origin
	} else {
		delete(g.RuleOrigins, risk.RuleAllowedTransactions)
	}
	if err := e.repo.UpsertServiceGuardrail(ctx, g); err != nil {
		return err
	}
	// The service's guardrail card offers only what is left to enforce.
	e.recommendations.requestMineService(namespace, service)
	return e.ReloadGuardrailConfig(ctx)
}

// DisableTransactionGuardrail disables allowed_transactions (mode off) so
// auto-enable will not immediately re-enable it. Other guardrail rules are kept.
func (e *Engine) DisableTransactionGuardrail(ctx context.Context, namespace, service string) error {
	scopeKey := repo.ServiceScopeKey(namespace, service)
	g, err := e.loadOrInitServiceGuardrail(ctx, scopeKey)
	if err != nil {
		return err
	}
	if g.RuleModes == nil {
		g.RuleModes = map[repo.RuleKey]repo.RuleMode{}
	}
	g.RuleModes[risk.RuleAllowedTransactions] = repo.RuleModeOff
	if err := e.repo.UpsertServiceGuardrail(ctx, g); err != nil {
		return err
	}
	e.recommendations.requestMineService(namespace, service)
	return e.ReloadGuardrailConfig(ctx)
}

// PromoteTransactionBaselines force-promotes every scored class of one
// transaction, skipping learning-phase gates, then may auto-enable the
// service's allowed_transactions guardrail when every class is promoted.
func (e *Engine) PromoteTransactionBaselines(ctx context.Context, transactionID int64) error {
	tx, ok, err := e.repo.GetTransactionByID(ctx, transactionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("transaction %d not found", transactionID)
	}
	for _, class := range ScoredClasses() {
		if err := e.repo.PromoteBaselineClass(ctx, transactionID, class); err != nil {
			return fmt.Errorf("promote %s on txn %d: %w", class, transactionID, err)
		}
	}
	e.store.removeEntry(transactionID)
	e.recommendations.requestMine(transactionID)
	err = e.maybeAutoEnableTransactionGuardrail(ctx, tx.Namespace, tx.Service, transactionID)
	e.recommendations.requestMineService(tx.Namespace, tx.Service)
	return err
}

// PromoteTransactionsBaselines force-promotes every scored class on each listed
// transaction, then runs the auto-enable check once per distinct service rather
// than once per transaction. Every transaction of a service produces the same
// service-scoped check (and its ListTransactionStats read), so deduping it keeps
// a bulk promote to one check per service. Returns the number of fully promoted
// transactions, the number that failed, and the last error.
func (e *Engine) PromoteTransactionsBaselines(ctx context.Context, transactionIDs []int64) (int, int, error) {
	var done, failed int
	var lastErr error
	type svcKey struct{ namespace, service string }
	promotedBy := map[svcKey][]int64{}
	var order []svcKey
	for _, id := range transactionIDs {
		if id <= 0 {
			failed++
			lastErr = fmt.Errorf("invalid transaction id %d", id)
			continue
		}
		tx, ok, err := e.repo.GetTransactionByID(ctx, id)
		if err != nil {
			failed++
			lastErr = err
			continue
		}
		if !ok {
			failed++
			lastErr = fmt.Errorf("transaction %d not found", id)
			continue
		}
		promoteErr := func() error {
			for _, class := range ScoredClasses() {
				if err := e.repo.PromoteBaselineClass(ctx, id, class); err != nil {
					return fmt.Errorf("promote %s on txn %d: %w", class, id, err)
				}
			}
			return nil
		}()
		if promoteErr != nil {
			failed++
			lastErr = promoteErr
			continue
		}
		e.store.removeEntry(id)
		e.recommendations.requestMine(id)
		key := svcKey{tx.Namespace, tx.Service}
		if _, seen := promotedBy[key]; !seen {
			order = append(order, key)
		}
		promotedBy[key] = append(promotedBy[key], id)
		done++
	}
	for _, key := range order {
		if err := e.maybeAutoEnableTransactionGuardrail(ctx, key.namespace, key.service, promotedBy[key]...); err != nil {
			lastErr = err
		}
		e.recommendations.requestMineService(key.namespace, key.service)
	}
	return done, failed, lastErr
}

// ForcePromoteService promotes every scored class on every transaction of the
// service. Afterward, maybeAutoEnableTransactionGuardrail may create the
// transaction guardrail if the global auto setting is on and the service is
// not disabled.
func (e *Engine) ForcePromoteService(ctx context.Context, namespace, service string) error {
	txs, err := e.repo.ListTransactionStats(ctx, repo.TransactionListParams{
		Namespace: namespace,
		Service:   service,
		Limit:     10000,
	})
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(txs))
	for _, tx := range txs {
		ids = append(ids, tx.ID)
		for _, class := range ScoredClasses() {
			if err := e.repo.PromoteBaselineClass(ctx, tx.ID, class); err != nil {
				return fmt.Errorf("promote %s on txn %d: %w", class, tx.ID, err)
			}
			e.store.removeEntry(tx.ID)
		}
		e.recommendations.requestMine(tx.ID)
	}
	// Treat these txns as promoted even if ListTransactionStats still lags
	// behind the ClickHouse promote inserts (same race as natural promote).
	err = e.maybeAutoEnableTransactionGuardrail(ctx, namespace, service, ids...)
	e.recommendations.requestMineService(namespace, service)
	return err
}

func (e *Engine) loadOrInitServiceGuardrail(ctx context.Context, scopeKey string) (repo.ServiceGuardrail, error) {
	all, err := e.repo.ListServiceGuardrails(ctx)
	if err != nil {
		return repo.ServiceGuardrail{}, err
	}
	for _, g := range all {
		if g.Scope == repo.ScopeService && g.ScopeKey == scopeKey {
			return g, nil
		}
	}
	return repo.ServiceGuardrail{
		Scope:     repo.ScopeService,
		ScopeKey:  scopeKey,
		RuleLists: map[repo.RuleKey][]string{},
		RuleModes: map[repo.RuleKey]repo.RuleMode{},
	}, nil
}

func (e *Engine) serviceTransactionIdentities(ctx context.Context, namespace, service string) ([]string, error) {
	txs, err := e.repo.ListTransactionStats(ctx, repo.TransactionListParams{
		Namespace: namespace,
		Service:   service,
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}
	idents := make([]string, 0, len(txs))
	seen := map[string]struct{}{}
	for _, tx := range txs {
		id := repo.TransactionIdentity(tx.Kind, tx.Operation)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		idents = append(idents, id)
	}
	return idents, nil
}

// serviceInventoryReady reports whether every listed transaction of the service
// has finished learning (Promoted). treatAsPromoted covers txns that just
// promoted in memory but whose promote flag may not be flushed to the DB yet.
func serviceInventoryReady(txs []repo.TransactionStat, treatAsPromoted map[int64]struct{}) bool {
	if len(txs) == 0 {
		return false
	}
	for _, tx := range txs {
		if _, ok := treatAsPromoted[tx.ID]; ok {
			continue
		}
		if !tx.Promoted {
			return false
		}
	}
	return true
}

// maybeAutoEnableTransactionGuardrail creates an enforced allowed_transactions
// guardrail when the global setting is on, the service is fully promoted, and
// the operator has not disabled it.
func (e *Engine) maybeAutoEnableTransactionGuardrail(ctx context.Context, namespace, service string, treatAsPromoted ...int64) error {
	if !e.system.Get().AutoTransactionGuardrail {
		return nil
	}
	scopeKey := repo.ServiceScopeKey(namespace, service)
	g, err := e.loadOrInitServiceGuardrail(ctx, scopeKey)
	if err != nil {
		return err
	}
	if InventoryOptedOut(g) || InventoryProtected(g) {
		return nil
	}
	txs, err := e.repo.ListTransactionStats(ctx, repo.TransactionListParams{
		Namespace: namespace,
		Service:   service,
		Limit:     10000,
	})
	if err != nil {
		return err
	}
	treat := map[int64]struct{}{}
	for _, id := range treatAsPromoted {
		treat[id] = struct{}{}
	}
	if !serviceInventoryReady(txs, treat) {
		return nil
	}
	return e.enableTransactionGuardrail(ctx, namespace, service, repo.GuardrailOriginAutoTransactionGuardrail)
}

// maybeAutoEnableTransactionGuardrailAfterPromote runs after a transaction
// finishes learning all classes. The txn id is treated as promoted even before
// write-behind flush so the service can become ready on this same observation.
// The service's guardrail card is queued afterwards so it offers only what the
// automatic guardrail did not already enforce.
func (e *Engine) maybeAutoEnableTransactionGuardrailAfterPromote(ctx context.Context, namespace, service string, transactionID int64) {
	if err := e.maybeAutoEnableTransactionGuardrail(ctx, namespace, service, transactionID); err != nil {
		e.warn("auto-enable transaction guardrail", err)
	}
	e.recommendations.requestMineService(namespace, service)
}

// resetInventoryGuardrails clears stale allowed_transactions guardrail state
// after a full inventory wipe. Enforced/auto rules are removed so relearned
// traffic is not policed with old identity strings; operator opt-out (mode off)
// is preserved. Stale forbidden-transaction findings are dropped from storage.
func (e *Engine) resetInventoryGuardrails(ctx context.Context) error {
	if err := e.repo.DeleteGuardrailViolationsForRule(ctx, risk.RuleAllowedTransactions); err != nil {
		return err
	}
	all, err := e.repo.ListServiceGuardrails(ctx)
	if err != nil {
		return err
	}
	changed := false
	for _, g := range all {
		if !clearInventoryGuardrailRule(&g) {
			continue
		}
		if err := e.repo.UpsertServiceGuardrail(ctx, g); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return e.ReloadGuardrailConfig(ctx)
}

func clearInventoryGuardrailRule(g *repo.ServiceGuardrail) bool {
	mode, hasMode := g.RuleModes[risk.RuleAllowedTransactions]
	optedOut := hasMode && repo.NormalizeRuleMode(mode) == repo.RuleModeOff
	_, hasList := g.RuleLists[risk.RuleAllowedTransactions]
	_, hasOrigin := g.RuleOrigins[risk.RuleAllowedTransactions]
	_, hasFingerprint := g.RuleFingerprints[risk.RuleAllowedTransactions]
	if !hasList && !hasOrigin && !hasMode && !hasFingerprint {
		return false
	}
	delete(g.RuleLists, risk.RuleAllowedTransactions)
	delete(g.RuleOrigins, risk.RuleAllowedTransactions)
	delete(g.RuleFingerprints, risk.RuleAllowedTransactions)
	if optedOut {
		return hasList || hasOrigin || hasFingerprint
	}
	delete(g.RuleModes, risk.RuleAllowedTransactions)
	return true
}
