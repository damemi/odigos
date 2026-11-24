package postgres_test

import (
	"context"
	"math/rand"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// TestCallTreeResyncIsNoOp checks that syncing a call tree again stores each
// node once.
func TestCallTreeResyncIsNoOp(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	sourceID, txID := rand.Int63(), rand.Int63()
	node := func(nodeID, parent, function string) repo.CallTreeNode {
		return repo.CallTreeNode{
			SourceID:      sourceID,
			TransactionID: txID,
			SampleType:    "samples",
			NodeID:        nodeID,
			Parent:        parent,
			FunctionName:  function,
			FrameType:     "jvm",
		}
	}
	nodes := []repo.CallTreeNode{node("n1", "root", "main"), node("n2", "n1", "handle"), node("n1", "root", "main")}
	for i := 0; i < 2; i++ {
		if err := r.InsertCallTreeNodes(ctx, nodes); err != nil {
			t.Fatal(err)
		}
	}

	conn, err := pgx.Connect(ctx, testDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	var n int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM autopilot_call_tree_nodes WHERE source_id = $1 AND transaction_id = $2`,
		sourceID, txID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("stored nodes = %d, want 2", n)
	}
}

// TestListCallTreeTransactions reads a workload container's call trees: only
// its own source's, paged by CPU-sample call paths, named when the transaction
// is stored.
func TestListCallTreeTransactions(t *testing.T) {
	r := testRepo(t)
	ctx := context.Background()
	ns := uniqueService("calltree-ns")
	identity := map[string]string{
		"k8s.namespace.name":   ns,
		"k8s.container.name":   "app",
		"odigos.workload.kind": "Deployment",
		"odigos.workload.name": "claims",
	}
	named, err := r.UpsertTransaction(ctx, "claims", ns, "POST /claims", repo.KindHTTP, identity)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, testDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	var sourceID int64
	if err := conn.QueryRow(ctx, `SELECT source_id FROM k8s_source_identity WHERE namespace = $1`, ns).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}

	// unnamed has no transactions row yet; it has two sample roots, named one.
	unnamed := rand.Int63()
	node := func(source, tx int64, sampleType, nodeID, parent, function string) repo.CallTreeNode {
		return repo.CallTreeNode{SourceID: source, TransactionID: tx, SampleType: sampleType, NodeID: nodeID, Parent: parent, FunctionName: function, FrameType: "jvm"}
	}
	if err := r.InsertCallTreeNodes(ctx, []repo.CallTreeNode{
		node(sourceID, named.ID, "samples", "a", "root", "submit"),
		node(sourceID, named.ID, "samples", "b", "a", "validate"),
		node(sourceID, named.ID, "events", "a", "root", "submit"),
		node(sourceID, unnamed, "samples", "c", "root", "list"),
		node(sourceID, unnamed, "samples", "d", "root", "get"),
		// Another source's node for the same transaction stays out.
		node(sourceID+1, named.ID, "samples", "x", "root", "other"),
	}); err != nil {
		t.Fatal(err)
	}

	params := repo.CallTreeTransactionsParams{Namespace: ns, WorkloadKind: "Deployment", WorkloadName: "claims", ContainerName: "app"}
	got, err := r.ListCallTreeTransactions(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TransactionID != unnamed || got[1].TransactionID != named.ID {
		t.Fatalf("transactions = %+v, want unnamed (2 sample roots) then named", got)
	}
	if got[0].Operation != "" || len(got[0].Nodes) != 2 {
		t.Fatalf("unnamed = %+v", got[0])
	}
	if got[1].Operation != "POST /claims" || got[1].EntryKind != "HTTP" || len(got[1].Nodes) != 3 {
		t.Fatalf("named = %+v", got[1])
	}

	paged := params
	paged.Limit, paged.Offset = 1, 1
	if got, err := r.ListCallTreeTransactions(ctx, paged); err != nil || len(got) != 1 || got[0].TransactionID != named.ID {
		t.Fatalf("second page = %+v, %v", got, err)
	}
	byID := params
	byID.TransactionIDs = []int64{named.ID}
	if got, err := r.ListCallTreeTransactions(ctx, byID); err != nil || len(got) != 1 || got[0].TransactionID != named.ID {
		t.Fatalf("by id = %+v, %v", got, err)
	}
	none := params
	none.TransactionIDs = []int64{}
	if got, err := r.ListCallTreeTransactions(ctx, none); err != nil || len(got) != 0 {
		t.Fatalf("empty ids = %+v, %v", got, err)
	}
	other := params
	other.WorkloadName = "unknown"
	if got, err := r.ListCallTreeTransactions(ctx, other); err != nil || len(got) != 0 {
		t.Fatalf("unknown workload = %+v, %v", got, err)
	}
}
