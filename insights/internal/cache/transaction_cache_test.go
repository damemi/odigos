package cache

import (
	"context"
	"sync"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// countingRepo embeds the interface (so unused methods exist) and counts how
// many UpsertTransaction calls reach the backing store.
type countingRepo struct {
	repo.Repository
	mu    sync.Mutex
	calls int
	byKey map[string]repo.Transaction
	next  int64
}

func newCountingRepo() *countingRepo {
	return &countingRepo{byKey: map[string]repo.Transaction{}, next: 1}
}

func (c *countingRepo) UpsertTransaction(_ context.Context, service, namespace, operation string, kind repo.TransactionKind) (repo.Transaction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	k := service + "|" + namespace + "|" + operation + "|" + string(kind)
	if tx, ok := c.byKey[k]; ok {
		return tx, nil
	}
	tx := repo.Transaction{ID: c.next, Service: service, Namespace: namespace, Operation: operation, Kind: kind}
	c.next++
	c.byKey[k] = tx
	return tx, nil
}

func (c *countingRepo) GetTransactionByID(_ context.Context, transactionID int64) (repo.Transaction, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tx := range c.byKey {
		if tx.ID == transactionID {
			return tx, true, nil
		}
	}
	return repo.Transaction{}, false, nil
}

func (c *countingRepo) DeleteTransaction(_ context.Context, transactionID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, tx := range c.byKey {
		if tx.ID == transactionID {
			delete(c.byKey, k)
			return nil
		}
	}
	return nil
}

func TestTxnCache_ServesRepeatsFromMemory(t *testing.T) {
	inner := newCountingRepo()
	c := WrapTransactions(inner)
	ctx := context.Background()

	// First lookup is a miss → hits the repo.
	tx1, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Many repeats are all hits → no further repo calls.
	for i := 0; i < 100; i++ {
		got, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP)
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
	if _, err := c.UpsertTransaction(ctx, "user-svc", "ns", "GET /cart", repo.KindHTTP); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected 2 backing calls after a new identity, got %d", inner.calls)
	}
}

// TestTxnCache_KeyedByAllIdentityFields guards that service, namespace,
// operation, AND kind each participate in the cache key (so e.g. an HTTP vs
// GRPC same-name op, or the same service in two namespaces, are not conflated).
func TestTxnCache_KeyedByAllIdentityFields(t *testing.T) {
	inner := newCountingRepo()
	c := WrapTransactions(inner)
	ctx := context.Background()

	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op", repo.KindHTTP)
	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op", repo.KindGRPC)  // kind differs
	_, _ = c.UpsertTransaction(ctx, "svc", "ns", "op2", repo.KindHTTP) // operation differs
	_, _ = c.UpsertTransaction(ctx, "svc2", "ns", "op", repo.KindHTTP) // service differs
	_, _ = c.UpsertTransaction(ctx, "svc", "ns2", "op", repo.KindHTTP) // namespace differs

	if inner.calls != 5 {
		t.Fatalf("expected 5 distinct identities, got %d backing calls", inner.calls)
	}
}

func TestTxnCache_InvalidatesOnDelete(t *testing.T) {
	inner := newCountingRepo()
	c := WrapTransactions(inner)
	ctx := context.Background()

	tx, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 backing call before delete, got %d", inner.calls)
	}

	if err := c.DeleteTransaction(ctx, tx.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.UpsertTransaction(ctx, "user-svc", "ns", "POST /checkout", repo.KindHTTP); err != nil {
		t.Fatalf("upsert after delete: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected backing UpsertTransaction after delete, got %d calls", inner.calls)
	}
}
