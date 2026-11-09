// Package cache holds in-memory layers that keep repeated work off the
// database.
//
// Transaction identity is resolved in-process by the engine (deterministic
// hash) and persisted on the write-behind flush via UpsertTransactions - see
// engine.processOne / baselineStore.flush. WrapTransactions remains available
// for tests and optional cold-path callers, but main does not wrap the hot path
// with it (a stale per-pod cache after DeleteTransaction caused missing UI rows
// under multi-replica traffic).
//
// This file caches the transaction lookup: given a (service, namespace,
// operation, kind), it returns the matching transaction row. Identity fields
// never change after creation, but DeleteTransaction evicts the entry so a
// later UpsertTransaction re-inserts the row in storage.
//
// Metrics:
//   - insights_txn_cache_lookups_total{result="hit|miss"} - cache hit ratio.
//   - On a miss we fall through to UpsertTransaction, which the metrics layer
//     counts under insights_repo_calls_total{method="UpsertTransaction"}.
package cache

import (
	"context"
	"sync"

	"github.com/odigos-io/odigos/insights/internal/metrics"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// WrapTransactions returns a repository that serves the transaction lookup
// from memory after the first time it sees each (service, operation, kind).
// Prefer the engine's write-behind identity path for hot traffic; this wrapper
// is not installed in main.
func WrapTransactions(wrapped repo.Repository) repo.Repository {
	return &transactionCache{
		Repository: wrapped,
		byKey:      make(map[transactionKey]repo.Transaction),
	}
}

type transactionKey struct {
	service   string
	namespace string
	operation string
	kind      repo.TransactionKind
}

// transactionCache embeds repo.Repository so every other method passes straight
// through to the wrapped repository; only the transaction lookup is cached.
type transactionCache struct {
	repo.Repository

	mu    sync.RWMutex
	byKey map[transactionKey]repo.Transaction
}

func (c *transactionCache) UpsertTransaction(ctx context.Context, service, namespace, operation string, kind repo.TransactionKind) (repo.Transaction, error) {
	k := transactionKey{service: service, namespace: namespace, operation: operation, kind: kind}

	c.mu.RLock()
	tx, ok := c.byKey[k]
	c.mu.RUnlock()
	if ok {
		metrics.IncTransactionCache("hit")
		return tx, nil
	}

	// Not cached yet: look it up in the database. Two requests for the same
	// key may both look it up at once, which is fine - the lookup returns the
	// same row either way.
	tx, err := c.Repository.UpsertTransaction(ctx, service, namespace, operation, kind)
	if err != nil {
		return tx, err
	}
	metrics.IncTransactionCache("miss")

	c.mu.Lock()
	c.byKey[k] = tx
	c.mu.Unlock()
	return tx, nil
}

func (c *transactionCache) DeleteTransaction(ctx context.Context, transactionID int64) error {
	tx, ok, err := c.Repository.GetTransactionByID(ctx, transactionID)
	if err != nil {
		return err
	}
	if err := c.Repository.DeleteTransaction(ctx, transactionID); err != nil {
		return err
	}
	c.evictTransaction(transactionID, tx, ok)
	return nil
}

func (c *transactionCache) DeleteAllTransactions(ctx context.Context) error {
	if err := c.Repository.DeleteAllTransactions(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.byKey = map[transactionKey]repo.Transaction{}
	c.mu.Unlock()
	return nil
}

func (c *transactionCache) evictTransaction(transactionID int64, tx repo.Transaction, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if known {
		delete(c.byKey, transactionKey{
			service: tx.Service, namespace: tx.Namespace,
			operation: tx.Operation, kind: tx.Kind,
		})
		return
	}
	for k, v := range c.byKey {
		if v.ID == transactionID {
			delete(c.byKey, k)
		}
	}
}
