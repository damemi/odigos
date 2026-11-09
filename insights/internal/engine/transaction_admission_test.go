package engine

import (
	"context"
	"fmt"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func serverTrace(service, operation string) *Trace {
	return mkTrace(withRoot(operation, service, tracepb.Span_SPAN_KIND_SERVER, nil))
}

// TestAdmissionDropsUnseenOverCap: once a service holds the cap, an operation
// the inventory has not seen is dropped before anything is stored, while the
// known ones keep being learned and counted.
func TestAdmissionDropsUnseenOverCap(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{MaxTransactionsPerService: 2}))

	eng.Process(ctx, serverTrace("api", "GET /users/1"))
	eng.Process(ctx, serverTrace("api", "GET /users/2"))
	eng.Process(ctx, serverTrace("api", "GET /users/3"))
	eng.Process(ctx, serverTrace("api", "GET /users/1"))
	// Another service is not affected by api being full.
	eng.Process(ctx, serverTrace("billing", "POST /charge"))

	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := len(r.transactions); got != 3 {
		t.Fatalf("transactions persisted = %d, want 3 (2 for api, 1 for billing)", got)
	}
	for _, tx := range r.transactions {
		if tx.Operation == "GET /users/3" {
			t.Fatalf("over-cap operation was persisted: %+v", tx)
		}
	}
	known := repo.NewTransaction("api", "unknown", "GET /users/1", repo.KindHTTP)
	if r.activity[known.ID] != 2 {
		t.Errorf("known transaction activity = %d, want 2 (still counted after the cap)", r.activity[known.ID])
	}
	dropped := repo.NewTransaction("api", "unknown", "GET /users/3", repo.KindHTTP)
	if _, ok := r.activity[dropped.ID]; ok {
		t.Errorf("dropped transaction recorded activity")
	}
	if eng.admission.count(repo.ServiceScopeKey("unknown", "api")) != 2 {
		t.Errorf("admission index for api = %d, want 2", eng.admission.count(repo.ServiceScopeKey("unknown", "api")))
	}
}

// TestAdmissionSeededFromInventory: a replica that starts with the inventory
// already full does not admit an unseen operation, and identities the DB knows
// are admitted even if this replica never saw them.
func TestAdmissionSeededFromInventory(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	for i := 0; i < 3; i++ {
		r.transactions = append(r.transactions, repo.NewTransaction("api", "unknown", fmt.Sprintf("GET /orders/%d", i), repo.KindHTTP))
	}
	eng := New(r, nil, nil, nil)
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{MaxTransactionsPerService: 3}))
	if err := eng.SyncTransactionAdmission(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	eng.Process(ctx, serverTrace("api", "GET /orders/999"))
	eng.Process(ctx, serverTrace("api", "GET /orders/1"))
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := len(r.transactions); got != 3 {
		t.Fatalf("transactions = %d, want 3 (unseen operation dropped)", got)
	}
	known := repo.NewTransaction("api", "unknown", "GET /orders/1", repo.KindHTTP)
	if r.activity[known.ID] != 1 {
		t.Errorf("seeded identity activity = %d, want 1 (admitted without this replica seeing it before)", r.activity[known.ID])
	}
}

// TestAdmissionRaisingLimitResumes: the cap is read per trace from the live
// settings, so raising it lets the next unseen operation in.
func TestAdmissionRaisingLimitResumes(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{MaxTransactionsPerService: 1}))

	eng.Process(ctx, serverTrace("api", "GET /a"))
	eng.Process(ctx, serverTrace("api", "GET /b"))
	if got := eng.admission.count(repo.ServiceScopeKey("unknown", "api")); got != 1 {
		t.Fatalf("index = %d, want 1 before raising the limit", got)
	}

	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{MaxTransactionsPerService: 2}))
	eng.Process(ctx, serverTrace("api", "GET /b"))
	if got := eng.admission.count(repo.ServiceScopeKey("unknown", "api")); got != 2 {
		t.Fatalf("index = %d, want 2 after raising the limit", got)
	}
}

// TestAdmissionDeleteFreesSlot: deleting a transaction frees its slot, and a
// full wipe empties the index.
func TestAdmissionDeleteFreesSlot(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	eng := New(r, nil, nil, nil)
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{MaxTransactionsPerService: 1}))
	key := repo.ServiceScopeKey("unknown", "api")

	eng.Process(ctx, serverTrace("api", "GET /a"))
	if err := eng.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	eng.Process(ctx, serverTrace("api", "GET /b"))
	if got := eng.admission.count(key); got != 1 {
		t.Fatalf("index = %d, want 1", got)
	}

	a := repo.NewTransaction("api", "unknown", "GET /a", repo.KindHTTP)
	if err := eng.DeleteTransaction(ctx, a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := eng.admission.count(key); got != 0 {
		t.Fatalf("index after delete = %d, want 0", got)
	}
	eng.Process(ctx, serverTrace("api", "GET /b"))
	if got := eng.admission.count(key); got != 1 {
		t.Fatalf("index after re-admit = %d, want 1", got)
	}

	if err := eng.DeleteAllTransactions(ctx); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if got := eng.admission.count(key); got != 0 {
		t.Fatalf("index after wipe = %d, want 0", got)
	}
}

func TestTransactionAdmissionNoLimit(t *testing.T) {
	a := newTransactionAdmission()
	for i := int64(0); i < 1000; i++ {
		if !a.admit("svc", i, 0) {
			t.Fatalf("limit 0 must admit everything, rejected id %d", i)
		}
	}
	if a.count("svc") != 1000 {
		t.Fatalf("count = %d, want 1000", a.count("svc"))
	}
}
