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
// This file caches the transaction lookup, keyed by the transaction id hashed
// from (service, namespace, operation, kind, source identity): it returns the
// matching transaction row. Identity fields never change after creation, but
// DeleteTransaction evicts the entry so a later UpsertTransaction re-inserts
// the row in storage.
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
// from memory after the first time it sees each transaction identity.
// Prefer the engine's write-behind identity path for hot traffic; this wrapper
// is not installed in main.
func WrapTransactions(wrapped repo.Repository) repo.Repository {
	return &transactionCache{
		Repository: wrapped,
		byID:       make(map[int64]repo.Transaction),
	}
}

// transactionCache embeds repo.Repository so every other method passes straight
// through to the wrapped repository; only the transaction lookup is cached.
type transactionCache struct {
	repo.Repository

	mu   sync.RWMutex
	byID map[int64]repo.Transaction
}

func (c *transactionCache) UpsertTransaction(ctx context.Context, service, namespace, operation string, kind repo.TransactionKind, sourceIdentity map[string]string) (repo.Transaction, error) {
	// The id hash covers every identity field, source identity included, so it
	// is the cache key.
	id := repo.HashTransactionID(service, namespace, operation, kind, sourceIdentity)

	c.mu.RLock()
	tx, ok := c.byID[id]
	c.mu.RUnlock()
	if ok {
		metrics.IncTransactionCache("hit")
		return tx, nil
	}

	// Not cached yet: look it up in the database. Two requests for the same
	// key may both look it up at once, which is fine - the lookup returns the
	// same row either way.
	tx, err := c.Repository.UpsertTransaction(ctx, service, namespace, operation, kind, sourceIdentity)
	if err != nil {
		return tx, err
	}
	metrics.IncTransactionCache("miss")

	c.mu.Lock()
	c.byID[id] = tx
	c.mu.Unlock()
	return tx, nil
}

func (c *transactionCache) DeleteTransaction(ctx context.Context, transactionID int64) error {
	if err := c.Repository.DeleteTransaction(ctx, transactionID); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.byID, transactionID)
	c.mu.Unlock()
	return nil
}

func (c *transactionCache) DeleteAllTransactions(ctx context.Context) error {
	if err := c.Repository.DeleteAllTransactions(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.byID = map[int64]repo.Transaction{}
	c.mu.Unlock()
	return nil
}
