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
