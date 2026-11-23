package autopilot

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// The flush copies every call tree of a stored sample type, in one insert.
func TestFlushCopiesAllCallTrees(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()

	samplesKey := redisCallTreeKey(42, "samples", "1")
	eventsKey := redisCallTreeKey(42, "events", "2")
	cpuKey := redisCallTreeKey(42, "cpu", "3")
	for _, key := range []string{samplesKey, eventsKey, cpuKey} {
		if err := rdb.HSet(ctx, key, sha1Hex("root\x00jvm\x00com.app.A"), "root\x00jvm\x00com.app.A").Err(); err != nil {
			t.Fatal(err)
		}
	}

	st := &fakeCallTreeNodeStore{}
	f := &callTreeFlusher{redis: &tracesRedis{rdb: rdb}, store: st, logger: commonlogger.LoggerCompat()}
	f.Flush(ctx)

	inserts := st.snapshot()
	if len(inserts) != 1 {
		t.Fatalf("inserts = %d, want 1", len(inserts))
	}
	got := map[string]bool{}
	for _, n := range inserts[0] {
		got[n.SampleType] = true
	}
	if len(inserts[0]) != 2 || !got["samples"] || !got["events"] {
		t.Fatalf("flushed nodes = %+v, want one samples and one events node", inserts[0])
	}
}
