package engine

import (
	"context"
	"sync"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// transactionAdmission is the per-service inventory index behind the
// max_transactions_per_service setting. A transaction identity that is already
// in the inventory is always admitted; an unseen one is admitted only while its
// service still has room, otherwise the trace is dropped before anything about
// it is stored. Without this, a span name that carries high cardinality (e.g. request id) mints a new
// transaction per request, and each one is persisted (transactions, activity,
// baselines) with no TTL.
//
// Every replica keeps its own copy, seeded from the transactions table at
// startup and merged with it on every settings reload, so admissions made on
// one replica reach the others within a flush plus a reload interval. A
// service can therefore briefly exceed the cap by a few identities across
// replicas; the cap is a bound on growth, not an exact count.
type transactionAdmission struct {
	mu        sync.RWMutex
	byService map[string]map[int64]struct{}
}

func newTransactionAdmission() *transactionAdmission {
	return &transactionAdmission{byService: map[string]map[int64]struct{}{}}
}

// admit reports whether transaction id of serviceKey may enter the inventory,
// recording it when it does. limit <= 0 disables the cap.
func (a *transactionAdmission) admit(serviceKey string, id int64, limit int) bool {
	a.mu.RLock()
	_, known := a.byService[serviceKey][id]
	a.mu.RUnlock()
	if known {
		return true
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	ids := a.byService[serviceKey]
	if _, known := ids[id]; known {
		return true
	}
	if limit > 0 && len(ids) >= limit {
		return false
	}
	if ids == nil {
		ids = map[int64]struct{}{}
		a.byService[serviceKey] = ids
	}
	ids[id] = struct{}{}
	return true
}

// merge adds every ref to the index. Used to seed from, and re-sync with, the
// persisted inventory; it never removes entries, so a delete must go through
// forget or reset.
func (a *transactionAdmission) merge(refs []repo.TransactionRef) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ref := range refs {
		key := repo.ServiceScopeKey(ref.Namespace, ref.Service)
		ids := a.byService[key]
		if ids == nil {
			ids = map[int64]struct{}{}
			a.byService[key] = ids
		}
		ids[ref.ID] = struct{}{}
	}
}

// forget removes one transaction so a deleted identity frees its slot. Cold
// path (admin delete), so the scan over services is fine.
func (a *transactionAdmission) forget(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, ids := range a.byService {
		if _, ok := ids[id]; ok {
			delete(ids, id)
			if len(ids) == 0 {
				delete(a.byService, key)
			}
			return
		}
	}
}

func (a *transactionAdmission) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byService = map[string]map[int64]struct{}{}
}

// contains reports whether id of serviceKey is in the index, without admitting it.
func (a *transactionAdmission) contains(serviceKey string, id int64) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.byService[serviceKey][id]
	return ok
}

// count returns how many transactions of serviceKey the index knows about.
func (a *transactionAdmission) count(serviceKey string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.byService[serviceKey])
}

// SyncTransactionAdmission folds the persisted inventory into the admission
// index. Called at startup and on every system-settings reload, so identities
// admitted on other replicas count here too. Until the first successful sync
// the index only knows what this replica admitted, which can only over-admit,
// never drop a known identity.
func (e *Engine) SyncTransactionAdmission(ctx context.Context) error {
	refs, err := e.repo.ListTransactionRefs(ctx)
	if err != nil {
		return err
	}
	e.admission.merge(refs)
	return nil
}
