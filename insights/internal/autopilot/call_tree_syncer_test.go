package autopilot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

type fakeCallTreeNodeStore struct {
	mu       sync.Mutex
	fail     bool
	attempts int
	inserts  [][]repo.CallTreeNode
	// block, when set, holds every insert until it is closed.
	block chan struct{}
}

func (f *fakeCallTreeNodeStore) InsertCallTreeNodes(ctx context.Context, nodes []repo.CallTreeNode) error {
	f.mu.Lock()
	f.attempts++
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("postgres down")
	}
	f.inserts = append(f.inserts, nodes)
	return nil
}

func (f *fakeCallTreeNodeStore) setFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

func (f *fakeCallTreeNodeStore) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func (f *fakeCallTreeNodeStore) snapshot() [][]repo.CallTreeNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]repo.CallTreeNode(nil), f.inserts...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestSyncer(t *testing.T, st CallTreeStore, delay, retryDelay time.Duration) (*callTreeSyncer, *redis.Client) {
	t.Helper()
	commonlogger.Init("error", "insights-test")
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := newCallTreeSyncer(&tracesRedis{rdb: rdb}, st, commonlogger.LoggerCompat(), delay, retryDelay)
	s.start()
	t.Cleanup(s.stop)
	return s, rdb
}

// Keys of one source that change within the delay are synced together, once.
func TestCallTreeSyncer_DebouncesPerSource(t *testing.T) {
	st := &fakeCallTreeNodeStore{}
	s, rdb := newTestSyncer(t, st, 100*time.Millisecond, time.Hour)
	ctx := context.Background()

	key1 := redisCallTreeKey(42, "samples", "1")
	key2 := redisCallTreeKey(42, "events", "2")
	_ = rdb.HSet(ctx, key1, sha1Hex("root\x00jvm\x00com.app.A"), "root\x00jvm\x00com.app.A").Err()
	_ = rdb.HSet(ctx, key2, sha1Hex("root\x00jvm\x00com.app.B"), "root\x00jvm\x00com.app.B").Err()

	s.changed([]string{key1})
	s.changed([]string{key2, key1})
	if got := len(st.snapshot()); got != 0 {
		t.Fatalf("inserts before the delay = %d, want 0", got)
	}

	waitFor(t, func() bool { return len(st.snapshot()) == 1 })
	if nodes := st.snapshot()[0]; len(nodes) != 2 {
		t.Fatalf("synced nodes = %d, want 2 (both keys in one sync)", len(nodes))
	}
	time.Sleep(200 * time.Millisecond)
	if got := len(st.snapshot()); got != 1 {
		t.Fatalf("inserts = %d, want 1", got)
	}
}

// A failed sync keeps its keys and retries them.
func TestCallTreeSyncer_RetriesFailedSync(t *testing.T) {
	st := &fakeCallTreeNodeStore{fail: true}
	s, rdb := newTestSyncer(t, st, 10*time.Millisecond, 50*time.Millisecond)

	key := redisCallTreeKey(42, "samples", "1")
	_ = rdb.HSet(context.Background(), key, sha1Hex("root\x00jvm\x00com.app.A"), "root\x00jvm\x00com.app.A").Err()

	s.changed([]string{key})
	waitFor(t, func() bool { return st.attemptCount() > 0 })
	st.setFail(false)
	waitFor(t, func() bool { return len(st.snapshot()) == 1 })
}

// Keys that are not call tree keys of a stored sample type never reach the store.
func TestCallTreeSyncer_IgnoresUnsyncableKeys(t *testing.T) {
	st := &fakeCallTreeNodeStore{}
	s, rdb := newTestSyncer(t, st, 10*time.Millisecond, time.Hour)
	ctx := context.Background()

	cpuKey := redisCallTreeKey(42, "cpu", "1")
	samplesKey := redisCallTreeKey(42, "samples", "1")
	_ = rdb.HSet(ctx, cpuKey, sha1Hex("root\x00jvm\x00com.app.Cpu"), "root\x00jvm\x00com.app.Cpu").Err()
	_ = rdb.HSet(ctx, samplesKey, sha1Hex("root\x00jvm\x00com.app.A"), "root\x00jvm\x00com.app.A").Err()

	s.changed([]string{redisCallTreeKeyPrefix + "42\x1fsamples", cpuKey, samplesKey})
	waitFor(t, func() bool { return len(st.snapshot()) == 1 })
	nodes := st.snapshot()[0]
	if len(nodes) != 1 || nodes[0].SampleType != "samples" {
		t.Fatalf("synced nodes = %+v, want only the samples node", nodes)
	}
}

// changed never blocks: with the queue full, keys are dropped.
func TestCallTreeSyncer_DropsWhenQueueFull(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	s := newCallTreeSyncer(nil, &fakeCallTreeNodeStore{}, commonlogger.LoggerCompat(), time.Hour, time.Hour)
	s.changes = make(chan []string, 1)

	s.changed(nil)
	if len(s.changes) != 0 {
		t.Fatal("an empty batch must not be queued")
	}
	s.changed([]string{redisCallTreeKey(42, "samples", "1")})
	s.changed([]string{redisCallTreeKey(42, "samples", "2"), redisCallTreeKey(42, "events", "2")})
	if len(s.changes) != 1 {
		t.Fatalf("queued batches = %d, want 1", len(s.changes))
	}
	if s.lastDropLog.IsZero() {
		t.Fatal("a dropped batch must be logged")
	}
}

// A sync that hangs does not stop the loop from draining the queue.
func TestCallTreeSyncer_SlowSyncDoesNotBlockQueue(t *testing.T) {
	st := &fakeCallTreeNodeStore{block: make(chan struct{})}
	s, rdb := newTestSyncer(t, st, 10*time.Millisecond, time.Hour)
	ctx := context.Background()

	key := redisCallTreeKey(42, "samples", "1")
	_ = rdb.HSet(ctx, key, sha1Hex("root\x00jvm\x00com.app.A"), "root\x00jvm\x00com.app.A").Err()
	s.changed([]string{key})
	waitFor(t, func() bool { return st.attemptCount() == 1 })

	for i := 0; i < 2*callTreeSyncQueueSize; i++ {
		s.changed([]string{redisCallTreeKey(int64(1000+i), "samples", "1")})
	}
	waitFor(t, func() bool { return len(s.changes) == 0 })
	close(st.block)
	waitFor(t, func() bool { return len(st.snapshot()) == 1 })
}
