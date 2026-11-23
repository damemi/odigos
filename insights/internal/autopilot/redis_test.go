package autopilot

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestMergeCallTrees_BuildsStructureWithoutCounts(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb, logger: nil}
	traceID := hex.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	spanID := []byte{9, 8, 7, 6, 5, 4, 3, 2}
	spanHex := hex.EncodeToString(spanID)

	blob, err := json.Marshal(stackBlob{
		SampleType: "samples",
		Frames: []stackBlobFrame{
			{FunctionName: "com.app.Root.run", FrameType: "jvm"},
			{FunctionName: "com.app.Leaf.run", FrameType: "jvm"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileKey := redisProfileKey(traceID)
	if err := rdb.HSet(context.Background(), profileKey, spanHex+":1", string(blob)).Err(); err != nil {
		t.Fatal(err)
	}

	tx := ServiceInvocation{
		SourceIdentity: deploymentSource("default", "checkout", "app"),
		TransactionID:  "nethttp@1.0 SERVER GET /buy",
		SpanIDs:        []string{spanHex},
	}

	changed, err := tr.mergeCallTrees(context.Background(), traceID, []ServiceInvocation{tx})
	if err != nil {
		t.Fatal(err)
	}

	callTreeKey := redisCallTreeKey(repo.HashSourceID(tx.SourceIdentity), "samples", tx.TransactionID)
	if len(changed) != 1 || changed[0] != callTreeKey {
		t.Fatalf("changed keys = %q, want [%q]", changed, callTreeKey)
	}
	fields, err := rdb.HGetAll(context.Background(), callTreeKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("call tree fields = %d, want 2; got %#v", len(fields), fields)
	}

	rootPayload := "root\x00jvm\x00com.app.Root.run"
	rootID := sha1Hex(rootPayload)
	leafPayload := rootID + "\x00jvm\x00com.app.Leaf.run"
	leafID := sha1Hex(leafPayload)

	if got := fields[rootID]; got != rootPayload {
		t.Fatalf("root node = %q, want %q", got, rootPayload)
	}
	if got := fields[leafID]; got != leafPayload {
		t.Fatalf("leaf node = %q, want %q", got, leafPayload)
	}
	if strings.Contains(fields[rootID], "samples") || strings.Contains(fields[rootID], "events") {
		t.Fatal("sample type must live in the redis key, not the hash value")
	}

	// Idempotent: same stacks do not add nodes again.
	changed, err = tr.mergeCallTrees(context.Background(), traceID, []ServiceInvocation{tx})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("second merge changed keys = %q, want none", changed)
	}
}

func TestMergeCallTrees_SeparateKeysPerSampleType(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb}
	traceID := hex.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	spanA := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	spanB := []byte{8, 7, 6, 5, 4, 3, 2, 1}

	samplesBlob, _ := json.Marshal(stackBlob{
		SampleType: "samples",
		Frames:     []stackBlobFrame{{FunctionName: "com.app.Samples", FrameType: "jvm"}},
	})
	eventsBlob, _ := json.Marshal(stackBlob{
		SampleType: "events",
		Frames:     []stackBlobFrame{{FunctionName: "com.app.Events", FrameType: "jvm"}},
	})
	_ = rdb.HSet(context.Background(), redisProfileKey(traceID),
		hex.EncodeToString(spanA)+":1", string(samplesBlob),
		hex.EncodeToString(spanB)+":2", string(eventsBlob),
	).Err()

	tx := ServiceInvocation{
		SourceIdentity: deploymentSource("ns", "svc", "c"), TransactionID: "tx",
		SpanIDs: []string{hex.EncodeToString(spanA), hex.EncodeToString(spanB)},
	}
	changed, err := tr.mergeCallTrees(context.Background(), traceID, []ServiceInvocation{tx})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed keys = %q, want one per sample type", changed)
	}

	samplesKey := redisCallTreeKey(repo.HashSourceID(tx.SourceIdentity), "samples", tx.TransactionID)
	eventsKey := redisCallTreeKey(repo.HashSourceID(tx.SourceIdentity), "events", tx.TransactionID)
	if !mr.Exists(samplesKey) || !mr.Exists(eventsKey) {
		t.Fatalf("expected both sample-type keys; samples=%v events=%v", mr.Exists(samplesKey), mr.Exists(eventsKey))
	}
	samplesFields, _ := rdb.HGetAll(context.Background(), samplesKey).Result()
	eventsFields, _ := rdb.HGetAll(context.Background(), eventsKey).Result()
	if len(samplesFields) != 1 || len(eventsFields) != 1 {
		t.Fatalf("samples=%d events=%d, want 1 each", len(samplesFields), len(eventsFields))
	}
}

func TestMergeCallTrees_IgnoresUnmatchedSpans(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb}
	traceID := hex.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	otherSpan := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	blob, _ := json.Marshal(stackBlob{
		SampleType: "events",
		Frames:     []stackBlobFrame{{FunctionName: "com.app.A", FrameType: "jvm"}},
	})
	_ = rdb.HSet(context.Background(), redisProfileKey(traceID),
		hex.EncodeToString(otherSpan)+":1", string(blob)).Err()

	tx := ServiceInvocation{
		SourceIdentity: deploymentSource("ns", "svc", "c"), TransactionID: "tx",
		SpanIDs: []string{"0202020202020202"},
	}
	changed, err := tr.mergeCallTrees(context.Background(), traceID, []ServiceInvocation{tx})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed keys = %q, want none", changed)
	}
	if mr.Exists(redisCallTreeKey(repo.HashSourceID(tx.SourceIdentity), "events", tx.TransactionID)) {
		t.Fatal("call tree key should not be created when no spans match")
	}
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func deploymentSource(namespace, name, container string) map[string]string {
	return map[string]string{
		"k8s.namespace.name":  namespace,
		"k8s.deployment.name": name,
		"k8s.container.name":  container,
	}
}

func TestMergeCallTrees_SkipsMissingSourceIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	tr := &tracesRedis{rdb: rdb}

	tx := ServiceInvocation{
		TransactionID: "tx",
		SpanIDs:       []string{"0101010101010101"},
	}
	changed, err := tr.mergeCallTrees(context.Background(), "01", []ServiceInvocation{tx})
	if err != nil || len(changed) != 0 {
		t.Fatalf("mergeCallTrees = %q, %v; want none, nil for an invocation without a source identity", changed, err)
	}
}

// One script call merges every invocation of the trace, each into its own call
// tree, from the stacks of its own spans only.
func TestMergeCallTrees_OneCallPerTrace(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	tr := &tracesRedis{rdb: rdb}

	traceID := hex.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	checkoutSpan, pricingSpan := "0101010101010101", "0202020202020202"
	checkoutBlob, _ := json.Marshal(stackBlob{SampleType: "samples", Frames: []stackBlobFrame{{FunctionName: "com.app.Checkout", FrameType: "jvm"}}})
	pricingBlob, _ := json.Marshal(stackBlob{SampleType: "samples", Frames: []stackBlobFrame{{FunctionName: "com.app.Pricing", FrameType: "jvm"}}})
	_ = rdb.HSet(context.Background(), redisProfileKey(traceID),
		checkoutSpan+":1", string(checkoutBlob),
		pricingSpan+":2", string(pricingBlob),
	).Err()

	invocations := []ServiceInvocation{
		{SourceIdentity: deploymentSource("shop", "checkout", "app"), TransactionID: "1", SpanIDs: []string{checkoutSpan}},
		{SourceIdentity: deploymentSource("shop", "pricing", "app"), TransactionID: "2", SpanIDs: []string{pricingSpan}},
	}
	changed, err := tr.mergeCallTrees(context.Background(), traceID, invocations)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed keys = %q, want one per invocation", changed)
	}

	for _, tc := range []struct{ workload, txID, function string }{
		{"checkout", "1", "com.app.Checkout"},
		{"pricing", "2", "com.app.Pricing"},
	} {
		key := redisCallTreeKey(repo.HashSourceID(deploymentSource("shop", tc.workload, "app")), "samples", tc.txID)
		fields, _ := rdb.HGetAll(context.Background(), key).Result()
		want := "root\x00jvm\x00" + tc.function
		if len(fields) != 1 || fields[sha1Hex(want)] != want {
			t.Errorf("%s call tree = %#v, want only %q", tc.workload, fields, want)
		}
		if ttl := mr.TTL(key); ttl != redisCallTreeKeyTTL {
			t.Errorf("%s call tree ttl = %v, want %v", tc.workload, ttl, redisCallTreeKeyTTL)
		}
	}
}

func TestParseRedisCallTreeKey(t *testing.T) {
	key := redisCallTreeKey(42, "samples", "123")
	got, ok := parseRedisCallTreeKey(key)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if got.SourceID != 42 || got.SampleType != "samples" || got.TransactionID != 123 {
		t.Fatalf("unexpected meta: %+v", got)
	}

	if _, ok := parseRedisCallTreeKey("odigos:autopilot:profile:abc"); ok {
		t.Fatal("profile key must not parse as call tree key")
	}
	if _, ok := parseRedisCallTreeKey(redisCallTreeKeyPrefix + "42\x1fsamples"); ok {
		t.Fatal("short key must not parse")
	}
	if _, ok := parseRedisCallTreeKey(redisCallTreeKeyPrefix + "42\x1f\x1f123"); ok {
		t.Fatal("empty segment must not parse")
	}
	if _, ok := parseRedisCallTreeKey(redisCallTreeKeyPrefix + "ns\x1fsamples\x1f123"); ok {
		t.Fatal("non-numeric source id must not parse")
	}
	if _, ok := parseRedisCallTreeKey(redisCallTreeKey(42, "samples", "GET /buy")); ok {
		t.Fatal("non-numeric transaction id must not parse")
	}
}

func TestParseCallTreeHashValue(t *testing.T) {
	parent, ft, name, ok := parseCallTreeHashValue("root\x00jvm\x00com.app.Root.run")
	if !ok || parent != "root" || ft != "jvm" || name != "com.app.Root.run" {
		t.Fatalf("got parent=%q ft=%q name=%q ok=%v", parent, ft, name, ok)
	}
	if _, _, _, ok := parseCallTreeHashValue("root\x00native\x00com.app.X"); ok {
		t.Fatal("non jvm/hotspot frame type must be rejected")
	}
	if _, _, _, ok := parseCallTreeHashValue("root\x00jvm"); ok {
		t.Fatal("short value must be rejected")
	}
}

func TestLoadCallTreeStructureNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb}
	key := redisCallTreeKey(42, "samples", "7")
	rootPayload := "root\x00jvm\x00com.app.Root"
	rootID := sha1Hex(rootPayload)
	leafPayload := rootID + "\x00jvm\x00com.app.Leaf"
	leafID := sha1Hex(leafPayload)
	if err := rdb.HSet(context.Background(), key, rootID, rootPayload, leafID, leafPayload).Err(); err != nil {
		t.Fatal(err)
	}
	_ = rdb.HSet(context.Background(), key, "badnode", "not-a-payload").Err()

	meta, ok := parseRedisCallTreeKey(key)
	if !ok {
		t.Fatal("expected parse ok")
	}
	nodes, err := tr.loadCallTreeStructureNodes(context.Background(), key, meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (malformed field skipped); got %#v", len(nodes), nodes)
	}

	byID := make(map[string]repo.CallTreeNode, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	root := byID[rootID]
	if root.Parent != "root" || root.FunctionName != "com.app.Root" || root.FrameType != "jvm" ||
		root.SampleType != "samples" || root.SourceID != 42 || root.TransactionID != 7 {
		t.Fatalf("root node unexpected: %+v", root)
	}
	leaf := byID[leafID]
	if leaf.Parent != rootID || leaf.FunctionName != "com.app.Leaf" {
		t.Fatalf("leaf node unexpected: %+v", leaf)
	}
}
