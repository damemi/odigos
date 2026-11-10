package clickhouse

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/db/clickhouse/migrate"
)

const defaultTestDSN = "clickhouse://insights:insightspass@localhost:19000/insights"

// testClient opens the interrogation client against CLICKHOUSE_TEST_DSN (the
// insights database) and its sibling interrogation database, migrating both.
// Skips when ClickHouse is not reachable (migrate is the first dial).
func testClient(t *testing.T) *Client {
	t.Helper()
	insightsDSN := os.Getenv("CLICKHOUSE_TEST_DSN")
	if insightsDSN == "" {
		insightsDSN = defaultTestDSN
	}
	u, err := url.Parse(insightsDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	insightsDB := u.Path[1:]
	u.Path = "/" + DatabaseName
	interrogationDSN := u.String()

	// migrate creates the interrogation database, so it runs before Ping.
	if err := migrate.Apply(insightsDSN, interrogationDSN); err != nil {
		t.Skipf("clickhouse not reachable at %s (%v)", insightsDSN, err)
	}
	c, err := Open(interrogationDSN, insightsDB)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (c *Client) insertInventory(t *testing.T, id int64, operation string, attrs map[string]string) {
	t.Helper()
	sql := fmt.Sprintf("INSERT INTO %q.%q (transaction_id, service, namespace, operation, kind, resource_attributes) VALUES (?, ?, ?, ?, ?, ?)",
		c.insightsDatabase, insightsTransactionsTable)
	if err := c.conn.Exec(context.Background(), sql, id, "svc", "ns", operation, "http", attrs); err != nil {
		t.Fatalf("insert inventory: %v", err)
	}
}

func (c *Client) insertTrieNode(t *testing.T, s SourceKey, txID int64) {
	t.Helper()
	sql := fmt.Sprintf("INSERT INTO %q.%q (Namespace, WorkloadKind, WorkloadName, ContainerName, TransactionId, SampleType, NodeId, Parent, FunctionName, FrameType) VALUES (?, ?, ?, ?, ?, 'samples', 'n1', 'root', 'main', 'jvm')",
		DatabaseName, callTrieStructureTable)
	if err := c.conn.Exec(context.Background(), sql,
		s.Namespace, s.WorkloadKind, s.WorkloadName, s.ContainerName, strconv.FormatInt(txID, 10)); err != nil {
		t.Fatalf("insert trie: %v", err)
	}
}

func hasSource(sources []SourceKey, want SourceKey) bool {
	for _, s := range sources {
		if s.Namespace == want.Namespace && s.WorkloadKind == want.WorkloadKind &&
			s.WorkloadName == want.WorkloadName && s.ContainerName == want.ContainerName {
			return true
		}
	}
	return false
}

func k8sAttrs(s SourceKey) map[string]string {
	return SourceKey{Namespace: s.Namespace, WorkloadKind: s.WorkloadKind, WorkloadName: s.WorkloadName, ContainerName: s.ContainerName}.lookupAttributes()
}

// TestSourcesComeFromTrie covers the loop's source and transaction discovery:
// a source with inventory rows but no trie is never listed, a transaction id
// shared by two workloads is listed under both (resource_attributes on the
// inventory row is last-write-wins), and DeleteSource takes a source out of
// the listing so it is not re-deleted every tick.
func TestSourcesComeFromTrie(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63())
	src := func(name string) SourceKey {
		return SourceKey{Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: name + "-" + suffix, ContainerName: "app"}
	}
	withTrie, noTrie, sharer := src("with-trie"), src("no-trie"), src("sharer")
	shared, inventoryOnly := rand.Int63(), rand.Int63()

	// The shared id's inventory row was last written by withTrie.
	c.insertInventory(t, shared, "GET /shared", k8sAttrs(withTrie))
	c.insertInventory(t, inventoryOnly, "GET /plain", k8sAttrs(noTrie))
	c.insertTrieNode(t, withTrie, shared)
	c.insertTrieNode(t, sharer, shared)

	sources, err := c.ListSourcesWithTransactions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasSource(sources, noTrie) {
		t.Fatal("source with transactions but no trie must not be listed")
	}
	if !hasSource(sources, withTrie) || !hasSource(sources, sharer) {
		t.Fatalf("trie sources missing from %+v", sources)
	}

	for _, s := range []SourceKey{withTrie, sharer} {
		txs, err := c.ListTransactions(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(txs) != 1 || txs[0].ID != strconv.FormatInt(shared, 10) || txs[0].RootSpanName != "GET /shared" {
			t.Fatalf("%s: transactions = %+v, want only the shared id", s.WorkloadName, txs)
		}
	}

	if err := c.DeleteSource(ctx, withTrie); err != nil {
		t.Fatal(err)
	}
	sources, err = c.ListSourcesWithTransactions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasSource(sources, withTrie) {
		t.Fatal("deleted source must drop out of the listing")
	}
	if !hasSource(sources, sharer) {
		t.Fatal("deleting one workload must not affect another sharing its transaction id")
	}
}

func TestSchemaReady(t *testing.T) {
	c := testClient(t)
	if err := c.SchemaReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	wrongDB := &Client{conn: c.conn, insightsDatabase: "no_such_db"}
	if err := wrongDB.SchemaReady(context.Background()); err == nil {
		t.Fatal("expected schema not ready when the insights database is not visible")
	}
}
