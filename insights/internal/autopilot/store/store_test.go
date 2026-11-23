package store

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/db/postgres/migrate"
)

const defaultTestDSN = "postgres://insights:insightspass@localhost:15432/insights?sslmode=disable"

// testClient opens the store against POSTGRES_TEST_DSN and applies the
// insights migrations. Skips when Postgres is not reachable.
func testClient(t *testing.T) *Client {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	c, err := Open(dsn)
	if err != nil {
		t.Skipf("postgres not configured (%v)", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("postgres not reachable at %s (%v)", dsn, err)
	}
	if err := migrate.Apply(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return c
}

// testSource returns a source unique to this test run.
func testSource(name string) SourceKey {
	return SourceKey{
		Namespace:     "ns",
		WorkloadKind:  "Deployment",
		WorkloadName:  fmt.Sprintf("%s-%d-%d", name, time.Now().UnixNano(), rand.Int63()),
		ContainerName: "app",
	}
}

func (c *Client) insertInventory(t *testing.T, id int64, operation string) {
	t.Helper()
	ctx := context.Background()
	if _, err := c.pool.Exec(ctx, `INSERT INTO sources (source_id, platform) VALUES ($1, 'unknown')`, id); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	if _, err := c.pool.Exec(ctx,
		`INSERT INTO transactions (transaction_id, source_id, service, operation, entry_kind) VALUES ($1, $1, 'svc', $2, 'HTTP')`,
		id, operation); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
}

// callTreeNode is one node to store with insertCallTree: {nodeID, parent, function}.
type callTreeNode [3]string

// insertCallTree stores a call tree for the k8s source s, as the call tree
// syncer and the baseline flush would: the source's sources and
// k8s_source_identity rows, and the nodes keyed by its source_id.
func (c *Client) insertCallTree(t *testing.T, s SourceKey, txID int64, nodes ...callTreeNode) {
	t.Helper()
	ctx := context.Background()
	h := fnv.New64a()
	_, _ = h.Write([]byte(s.Namespace + "\x00" + s.WorkloadKind + "\x00" + s.WorkloadName + "\x00" + s.ContainerName))
	sourceID := int64(h.Sum64() >> 1)
	if _, err := c.pool.Exec(ctx, `INSERT INTO sources (source_id, platform) VALUES ($1, 'k8s') ON CONFLICT DO NOTHING`, sourceID); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	if _, err := c.pool.Exec(ctx,
		`INSERT INTO k8s_source_identity (source_id, namespace, workload_kind, workload_name, container_name)
		 VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
		sourceID, s.Namespace, s.WorkloadKind, s.WorkloadName, s.ContainerName); err != nil {
		t.Fatalf("insert k8s source identity: %v", err)
	}
	for _, n := range nodes {
		if _, err := c.pool.Exec(ctx,
			`INSERT INTO autopilot_call_tree_nodes (source_id, transaction_id, sample_type, node_id, parent, function_name, frame_type)
			 VALUES ($1, $2, 'samples', $3, $4, $5, 'jvm') ON CONFLICT DO NOTHING`,
			sourceID, txID, n[0], n[1], n[2]); err != nil {
			t.Fatalf("insert call tree node: %v", err)
		}
	}
}

func hasSource(sources []SourceKey, want SourceKey) bool {
	for _, s := range sources {
		if s == want {
			return true
		}
	}
	return false
}

// TestSourcesComeFromCallTree covers the loop's source and transaction discovery:
// a source with inventory rows but no call tree is never listed, a transaction id
// shared by two workloads is listed under both (membership comes from the
// call tree, not from the inventory), and DeleteSource takes a source out of the
// listing so it is not re-deleted every tick.
func TestSourcesComeFromCallTree(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	withCallTree, noCallTree, sharer := testSource("with-call-tree"), testSource("no-call-tree"), testSource("sharer")
	shared, inventoryOnly := rand.Int63(), rand.Int63()

	c.insertInventory(t, shared, "GET /shared")
	c.insertInventory(t, inventoryOnly, "GET /plain")
	c.insertCallTree(t, withCallTree, shared, callTreeNode{"n1", "root", "main"})
	c.insertCallTree(t, sharer, shared, callTreeNode{"n1", "root", "main"})

	sources, err := c.ListSourcesWithTransactions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasSource(sources, noCallTree) {
		t.Fatal("source with transactions but no call tree must not be listed")
	}
	if !hasSource(sources, withCallTree) || !hasSource(sources, sharer) {
		t.Fatalf("call tree sources missing from %+v", sources)
	}

	for _, s := range []SourceKey{withCallTree, sharer} {
		txs, err := c.ListTransactions(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(txs) != 1 || txs[0].ID != strconv.FormatInt(shared, 10) || txs[0].RootSpanName != "GET /shared" || txs[0].RootSpanKind != "HTTP" {
			t.Fatalf("%s: transactions = %+v, want only the shared id", s.WorkloadName, txs)
		}
	}

	if err := c.DeleteSource(ctx, withCallTree); err != nil {
		t.Fatal(err)
	}
	sources, err = c.ListSourcesWithTransactions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasSource(sources, withCallTree) {
		t.Fatal("deleted source must drop out of the listing")
	}
	if !hasSource(sources, sharer) {
		t.Fatal("deleting one workload must not affect another sharing its transaction id")
	}
}

// TestGetCallTree checks that a call tree reads back by its workload, with
// roots read back without a parent.
func TestGetCallTree(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	s := testSource("get-call-tree")
	txID := rand.Int63()
	c.insertCallTree(t, s, txID, callTreeNode{"n1", "root", "main"}, callTreeNode{"n2", "n1", "handle"})

	got, err := c.GetCallTree(ctx, s, strconv.FormatInt(txID, 10))
	if err != nil {
		t.Fatal(err)
	}
	want := []CallTreeNode{
		{ID: "n2", ParentID: "n1", Name: "handle", FrameType: "jvm", SampleType: "samples"},
		{ID: "n1", ParentID: "", Name: "main", FrameType: "jvm", SampleType: "samples"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("call tree = %+v, want %+v", got, want)
	}
}

func TestLastSuccessfulRun(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	s := testSource("runs")
	txID := strconv.FormatInt(rand.Int63(), 10)
	base := time.Now().UTC().Truncate(time.Microsecond)

	got, err := c.LastSuccessfulRun(ctx, s, txID, "rev1")
	if err != nil || got != nil {
		t.Fatalf("no runs: got %+v, err %v", got, err)
	}

	run := func(at time.Time, status, revision, hash string) Run {
		return Run{
			Timestamp: at, Namespace: s.Namespace, WorkloadKind: s.WorkloadKind, WorkloadName: s.WorkloadName,
			ContainerName: s.ContainerName, TransactionID: txID, CallTreeHash: hash,
			AnalysisStatus: status, AnalysisRevision: revision,
		}
	}
	for _, r := range []Run{
		run(base, AnalysisSucceeded, "rev1", "old"),
		run(base.Add(time.Second), AnalysisSucceeded, "rev1", "newest"),
		run(base.Add(2*time.Second), AnalysisFailedTransient, "rev1", "failed"),
		run(base.Add(3*time.Second), AnalysisSucceeded, "rev2", "other-revision"),
	} {
		if err := c.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	got, err = c.LastSuccessfulRun(ctx, s, txID, "rev1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.CallTreeHash != "newest" || got.TransactionID != txID || !got.Timestamp.Equal(base.Add(time.Second)) {
		t.Fatalf("last successful run = %+v, want the newest succeeded rev1 run", got)
	}
}

func TestRecommendationsKeepFirst(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	s := testSource("recs")
	workload := SourceKey{Namespace: s.Namespace, WorkloadKind: s.WorkloadKind, WorkloadName: s.WorkloadName}
	base := time.Now().UTC().Truncate(time.Microsecond)
	rec := func(target, why string, at time.Time) Recommendation {
		return Recommendation{
			FirstSeen: at, Namespace: s.Namespace, WorkloadKind: s.WorkloadKind, WorkloadName: s.WorkloadName,
			ContainerName: s.ContainerName, TransactionID: "42", Language: "java", Target: target, Why: why,
		}
	}

	if err := c.InsertRecommendations(ctx, []Recommendation{rec("b", "first", base.Add(time.Second)), rec("a", "first", base)}); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertRecommendations(ctx, []Recommendation{rec("a", "repeat", base.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}

	got, err := c.ListRecommendations(ctx, workload)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Target != "a" || got[0].Why != "first" || got[1].Target != "b" || got[0].TransactionID != "42" {
		t.Fatalf("recommendations = %+v, want a then b, each from its first insert", got)
	}

	workloads, err := c.ListRecommendedWorkloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSource(workloads, workload) {
		t.Fatalf("recommended workloads %+v missing %+v", workloads, workload)
	}
}

func TestSchemaReady(t *testing.T) {
	c := testClient(t)
	if err := c.SchemaReady(context.Background()); err != nil {
		t.Fatal(err)
	}
}
