package repo

import "strconv"

// Policy scope is the shared "who does this policy apply to" concept, reused by
// learning policies, alert policies, and guardrails so they all resolve
// overrides the same way. Each has one global default plus narrower overrides,
// and the most specific match wins outright (transaction → service → global);
// a matching override fully replaces the broader one rather than merging.

// PolicyScope narrows a policy to a subset of transactions.
type PolicyScope string

const (
	// ScopeGlobal is the default: it applies to every transaction that has no
	// narrower override. Pre-scope rows read as global.
	ScopeGlobal PolicyScope = "global"
	// ScopeService applies to every transaction of one (namespace, service).
	// ScopeKey is ServiceScopeKey(namespace, service).
	ScopeService PolicyScope = "service"
	// ScopeTransaction applies to a single transaction. ScopeKey is the
	// transaction_id as base-10 text.
	ScopeTransaction PolicyScope = "transaction"
)

// NormalizePolicyScope maps an empty/unknown scope to ScopeGlobal, so a blank
// caller value or a pre-scope DB row always resolves to a real scope.
func NormalizePolicyScope(s PolicyScope) PolicyScope {
	switch s {
	case ScopeService, ScopeTransaction:
		return s
	default:
		return ScopeGlobal
	}
}

// ServiceScopeKey builds the ScopeKey for a ScopeService policy,
// "namespace/service", so the engine, repo, and UI agree on the format. A
// transaction scope's key is just the transaction_id as base-10 text.
func ServiceScopeKey(namespace, service string) string {
	return namespace + "/" + service
}

// TransactionScopeKey builds the ScopeKey for a ScopeTransaction policy.
func TransactionScopeKey(transactionID int64) string {
	return strconv.FormatInt(transactionID, 10)
}

// ScopeIndex is the shared "most specific wins" resolver for scoped policies of
// any value type T. Built once from the policy rows by the engine, then Resolve
// is a wait-free lookup (two map probes) safe for the per-trace path. It follows
// a build-then-publish pattern: not safe for concurrent Put during Resolve.
type ScopeIndex[T any] struct {
	global    T
	hasGlobal bool
	// byService is keyed by ServiceScopeKey(namespace, service).
	byService map[string]T
	// byTxn is keyed by transaction_id (ScopeKey parsed once at build time, so
	// the per-trace path does no string work).
	byTxn map[int64]T
}

// Put stores v under a scope. An empty/unknown scope is stored as global. A
// transaction scope whose key is not a valid transaction_id is dropped and Put
// returns false, since it could never be resolved.
func (i *ScopeIndex[T]) Put(scope PolicyScope, scopeKey string, v T) bool {
	switch NormalizePolicyScope(scope) {
	case ScopeService:
		if i.byService == nil {
			i.byService = make(map[string]T)
		}
		i.byService[scopeKey] = v
	case ScopeTransaction:
		id, err := strconv.ParseInt(scopeKey, 10, 64)
		if err != nil {
			return false
		}
		if i.byTxn == nil {
			i.byTxn = make(map[int64]T)
		}
		i.byTxn[id] = v
	default:
		i.global = v
		i.hasGlobal = true
	}
	return true
}

// Resolve returns the most specific value for a transaction: its own override,
// else its service's override, else the global default. ok is false when no
// scope matches (not even global). serviceKey is ServiceScopeKey(ns, service).
func (i *ScopeIndex[T]) Resolve(transactionID int64, serviceKey string) (T, bool) {
	if i == nil {
		var zero T
		return zero, false
	}
	if v, ok := i.byTxn[transactionID]; ok {
		return v, true
	}
	if serviceKey != "" {
		if v, ok := i.byService[serviceKey]; ok {
			return v, true
		}
	}
	if i.hasGlobal {
		return i.global, true
	}
	var zero T
	return zero, false
}

// Global returns the global default and whether one is set, for callers that
// want the class/base default independent of any override.
func (i *ScopeIndex[T]) Global() (T, bool) {
	if i == nil {
		var zero T
		return zero, false
	}
	return i.global, i.hasGlobal
}

// Each invokes fn for every policy stored in the index (global, service, and
// transaction scopes). Used by background sweeps that need to scan all loaded
// policies; not for the per-trace hot path.
func (i *ScopeIndex[T]) Each(fn func(T)) {
	if i == nil {
		return
	}
	if i.hasGlobal {
		fn(i.global)
	}
	for _, v := range i.byService {
		fn(v)
	}
	for _, v := range i.byTxn {
		fn(v)
	}
}
