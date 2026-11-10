package interrogation

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMergeCallTrie_BuildsStructureWithoutCounts(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb, logger: nil}
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
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

	tx := TraceTransaction{
		Timestamp:     time.Unix(1, 0).UTC(),
		Namespace:     "default",
		WorkloadKind:  "Deployment",
		WorkloadName:  "checkout",
		ContainerName: "app",
		TransactionID: "nethttp@1.0 SERVER GET /buy",
		SpanIDs:       []string{string(spanID)},
	}

	n, err := tr.mergeCallTrie(context.Background(), tx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("new nodes = %d, want 2", n)
	}

	trieKey := redisTrieKey(tx.Namespace, tx.WorkloadKind, tx.WorkloadName, tx.ContainerName, "samples", tx.TransactionID)
	fields, err := rdb.HGetAll(context.Background(), trieKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 {
		t.Fatalf("trie fields = %d, want 2; got %#v", len(fields), fields)
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
	n, err = tr.mergeCallTrie(context.Background(), tx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second merge new nodes = %d, want 0", n)
	}
}

func TestMergeCallTrie_SeparateKeysPerSampleType(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb}
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
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

	tx := TraceTransaction{
		Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "svc",
		ContainerName: "c", TransactionID: "tx",
		SpanIDs: []string{string(spanA), string(spanB)},
	}
	n, err := tr.mergeCallTrie(context.Background(), tx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("new nodes = %d, want 2", n)
	}

	samplesKey := redisTrieKey(tx.Namespace, tx.WorkloadKind, tx.WorkloadName, tx.ContainerName, "samples", tx.TransactionID)
	eventsKey := redisTrieKey(tx.Namespace, tx.WorkloadKind, tx.WorkloadName, tx.ContainerName, "events", tx.TransactionID)
	if !mr.Exists(samplesKey) || !mr.Exists(eventsKey) {
		t.Fatalf("expected both sample-type keys; samples=%v events=%v", mr.Exists(samplesKey), mr.Exists(eventsKey))
	}
	samplesFields, _ := rdb.HGetAll(context.Background(), samplesKey).Result()
	eventsFields, _ := rdb.HGetAll(context.Background(), eventsKey).Result()
	if len(samplesFields) != 1 || len(eventsFields) != 1 {
		t.Fatalf("samples=%d events=%d, want 1 each", len(samplesFields), len(eventsFields))
	}
}

func TestMergeCallTrie_IgnoresUnmatchedSpans(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tr := &tracesRedis{rdb: rdb}
	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	otherSpan := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	blob, _ := json.Marshal(stackBlob{
		SampleType: "events",
		Frames:     []stackBlobFrame{{FunctionName: "com.app.A", FrameType: "jvm"}},
	})
	_ = rdb.HSet(context.Background(), redisProfileKey(traceID),
		hex.EncodeToString(otherSpan)+":1", string(blob)).Err()

	tx := TraceTransaction{
		Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "svc",
		ContainerName: "c", TransactionID: "tx",
		SpanIDs: []string{string([]byte{2, 2, 2, 2, 2, 2, 2, 2})},
	}
	n, err := tr.mergeCallTrie(context.Background(), tx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("new nodes = %d, want 0", n)
	}
	if mr.Exists(redisTrieKey(tx.Namespace, tx.WorkloadKind, tx.WorkloadName, tx.ContainerName, "events", tx.TransactionID)) {
		t.Fatal("trie key should not be created when no spans match")
	}
}

func sha1Hex(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
