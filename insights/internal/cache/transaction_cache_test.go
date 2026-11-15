package cache

import (
	"context"
	"sync"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// countingRepo embeds the interface (so unused methods exist) and counts how
// many UpsertTransaction calls reach the backing store. Like the real
// repository, it identifies a transaction by its hashed id.
type countingRepo struct {
	repo.Repository
	mu    sync.Mutex
	calls int
	byID  map[int64]repo.Transaction
}

func newCountingRepo() *countingRepo {
	return &countingRepo{byID: map[int64]repo.Transaction{}}
}

func (c *countingRepo) UpsertTransaction(_ context.Context, service, namespace, operation string, kind repo.TransactionKind, sourceIdentity map[string]string) (repo.Transaction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	tx := repo.NewTransaction(service, namespace, operation, kind, sourceIdentity)
	if existing, ok := c.byID[tx.ID]; ok {
		return existing, nil
	}
	c.byID[tx.ID] = tx
	return tx, nil
}

func (c *countingRepo) DeleteTransaction(_ context.Context, transactionID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, transactionID)
	return nil
}

func TestTxnCache_ServesRepeatsFromMemory(t *testing.T) {
	inner := newCountingRepo()
	c := WrapTransactions(inner)
	ctx := context.Background()

	// First lookup is a miss → hits the repo.
	tx1, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Many repeats are all hits → no further repo calls.
	for i := 0; i < 100; i++ {
		got, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP, nil)
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got.ID != tx1.ID {
			t.Fatalf("cache returned wrong identity: got %d want %d", got.ID, tx1.ID)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 backing UpsertTransaction call, got %d", inner.calls)
	}

	// A different identity is a distinct miss.
	if _, err := c.UpsertTransaction(ctx, "user-svc", "ns", "GET /cart", repo.KindHTTP, nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected 2 backing calls after a new identity, got %d", inner.calls)
	}
}

// TestTxnCache_KeyedByAllIdentityFields guards that service, namespace,
// operation, kind AND source identity each participate in the cache key (so
// e.g. an HTTP vs GRPC same-name op, the same service in two namespaces, or
// the same op in two workloads, are not conflated).
func TestTxnCache_KeyedByAllIdentityFields(t *testing.T) {
	inner := newCountingRepo()
	c := WrapTransactions(inner)
	ctx := context.Background()

	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op", repo.KindHTTP, nil)
	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op", repo.KindGRPC, nil)  // kind differs
	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op2", repo.KindHTTP, nil) // operation differs
	_, _ = c.UpsertTransaction(ctx, "svc2", "ns", "op", repo.KindHTTP, nil) // service differs
	_, _ = c.UpsertTransaction(ctx, "svc", "ns2", "op", repo.KindHTTP, nil) // namespace differs
	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op", repo.KindHTTP,
		map[string]string{"odigos.workload.name": "a"}) // source identity differs
	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op", repo.KindHTTP, map[string]string{}) // empty = nil

	if inner.calls != 6 {
		t.Fatalf("expected 6 distinct identities, got %d backing calls", inner.calls)
	}
}

func TestTxnCache_InvalidatesOnDelete(t *testing.T) {
	inner := newCountingRepo()
	c := WrapTransactions(inner)
	ctx := context.Background()

	tx, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP, nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 backing call before delete, got %d", inner.calls)
	}

	if err := c.DeleteTransaction(ctx, tx.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP, nil); err != nil {
		t.Fatalf("upsert after delete: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected backing UpsertTransaction after delete, got %d calls", inner.calls)
	}
}
