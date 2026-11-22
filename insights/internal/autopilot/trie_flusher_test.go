package autopilot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseRedisTrieKey(t *testing.T) {
	key := redisTrieKey("default", "Deployment", "checkout", "app", "samples", "nethttp@1.0 SERVER GET /buy")
	got, ok := parseRedisTrieKey(key)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if got.Namespace != "default" || got.WorkloadKind != "Deployment" || got.WorkloadName != "checkout" ||
		got.ContainerName != "app" || got.SampleType != "samples" || got.TransactionID != "nethttp@1.0 SERVER GET /buy" {
		t.Fatalf("unexpected meta: %+v", got)
	}

	if _, ok := parseRedisTrieKey("odigos:autopilot:profile:abc"); ok {
		t.Fatal("profile key must not parse as trie key")
	}
	if _, ok := parseRedisTrieKey(redisTrieKeyPrefix + "a\x1fb\x1fc"); ok {
		t.Fatal("short key must not parse")
	}
	if _, ok := parseRedisTrieKey(redisTrieKeyPrefix + "a\x1fb\x1fc\x1f\x1fsamples\x1ftx"); ok {
		t.Fatal("empty segment must not parse")
	}
}

func TestParseTrieHashValue(t *testing.T) {
	parent, ft, name, ok := parseTrieHashValue("root\x00jvm\x00com.app.Root.run")
	if !ok || parent != "root" || ft != "jvm" || name != "com.app.Root.run" {
		t.Fatalf("got parent=%q ft=%q name=%q ok=%v", parent, ft, name, ok)
	}
	if _, _, _, ok := parseTrieHashValue("root\x00native\x00com.app.X"); ok {
		t.Fatal("non jvm/hotspot frame type must be rejected")
	}
	if _, _, _, ok := parseTrieHashValue("root\x00jvm"); ok {
		t.Fatal("short value must be rejected")
	}
}

func TestScanAndLoadTrieStructureNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb}
	key := redisTrieKey("ns", "Deployment", "svc", "c", "samples", "tx-1")
	rootPayload := "root\x00jvm\x00com.app.Root"
	rootID := sha1Hex(rootPayload)
	leafPayload := rootID + "\x00jvm\x00com.app.Leaf"
	leafID := sha1Hex(leafPayload)
	if err := rdb.HSet(context.Background(), key, rootID, rootPayload, leafID, leafPayload).Err(); err != nil {
		t.Fatal(err)
	}
	// Noise key that must be ignored by SCAN match... actually SCAN match is prefix*;
	// add a malformed trie key and a bad hash value to exercise skips.
	badKey := redisTrieKeyPrefix + "only\x1ftwo"
	_ = rdb.HSet(context.Background(), badKey, "x", "bad").Err()
	_ = rdb.HSet(context.Background(), key, "badnode", "not-a-payload").Err()

	keys, err := tr.scanTrieKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) < 1 {
		t.Fatalf("scan keys = %d, want at least 1", len(keys))
	}

	meta, ok := parseRedisTrieKey(key)
	if !ok {
		t.Fatal("expected parse ok")
	}
	nodes, err := tr.loadTrieStructureNodes(context.Background(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (malformed field skipped); got %#v", len(nodes), nodes)
	}

	byID := make(map[string]store.FlushedTrieNode, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	root := byID[rootID]
	if root.Parent != "root" || root.FunctionName != "com.app.Root" || root.FrameType != "jvm" ||
		root.SampleType != "samples" || root.Namespace != "ns" || root.TransactionID != "tx-1" {
		t.Fatalf("root node unexpected: %+v", root)
	}
	leaf := byID[leafID]
	if leaf.Parent != rootID || leaf.FunctionName != "com.app.Leaf" {
		t.Fatalf("leaf node unexpected: %+v", leaf)
	}

	if _, ok := parseRedisTrieKey(badKey); ok {
		t.Fatal("malformed key must be rejected by the caller before load")
	}
}

func TestFlushSkipsWorkloadWithoutInstrumentationConfig(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	key := redisTrieKey("shop", "Deployment", "checkout", "app", "samples", "tx-1")
	if err := rdb.HSet(context.Background(), key, "node", "root\x00jvm\x00com.app.Root").Err(); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := odigosv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := &trieFlusher{
		redis: &tracesRedis{rdb: rdb},
		kube:  fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
	f.Flush(context.Background())
	n, err := rdb.Exists(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("missing instrumentation config must delete the redis key")
	}

	f.kube = fake.NewClientBuilder().WithScheme(scheme).WithObjects(&odigosv1.InstrumentationConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deployment-checkout"},
	}).Build()
	meta, ok := parseRedisTrieKey(key)
	if !ok {
		t.Fatal("expected parse ok")
	}
	present, err := f.workloadInstrumented(context.Background(), meta)
	if err != nil || !present {
		t.Fatalf("present=%v err=%v", present, err)
	}
}
