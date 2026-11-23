package autopilot

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// Insights may start before cacheDb Redis is up. The client must not be
// permanently unusable: once Redis listens on the endpoint, commands succeed.
func TestTracesRedis_RecoversWhenRedisStartsLater(t *testing.T) {
	// Reserve a free port, then release it so nothing listens yet.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	r := newTracesRedis(addr, nil)
	t.Cleanup(func() { _ = r.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.ping(ctx); err == nil {
		t.Fatal("ping succeeded with no redis listening")
	}

	mr := miniredis.NewMiniRedis()
	if err := mr.StartAddr(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := r.ping(ctx2); err != nil {
		t.Fatalf("ping after redis started: %v", err)
	}
	tx := ServiceInvocation{SourceIdentity: deploymentSource("ns", "svc", "c"), TransactionID: "1", SpanIDs: []string{"01"}}
	if _, err := r.mergeCallTrees(ctx2, "01", []ServiceInvocation{tx}); err != nil {
		t.Fatalf("merge after redis started: %v", err)
	}
}

// Open must not fail just because Redis is unreachable at startup.
func TestOpen_SucceedsWithUnreachableRedis(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	c, err := Open("127.0.0.1:1", &fakeCallTreeNodeStore{}, commonlogger.LoggerCompat())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

func TestOpen_RejectsMissingConfig(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	if _, err := Open("", &fakeCallTreeNodeStore{}, commonlogger.LoggerCompat()); err == nil {
		t.Fatal("expected error for empty redis endpoint")
	}
	if _, err := Open("127.0.0.1:1", nil, commonlogger.LoggerCompat()); err == nil {
		t.Fatal("expected error for a nil call tree store")
	}
}

func TestReportFlush_ThrottlesAndResets(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	b := newInvocationBatcher(0, 0, commonlogger.LoggerCompat())
	start := time.Unix(1000, 0)
	errDown := errors.New("redis down")

	b.reportFlush(errDown, 3, start)
	if !b.lastFailureLog.Equal(start) {
		t.Fatalf("first failure should log, lastFailureLog = %v", b.lastFailureLog)
	}
	b.reportFlush(errDown, 2, start.Add(5*time.Second))
	if !b.lastFailureLog.Equal(start) {
		t.Fatal("second failure within failureLogEvery must not log")
	}
	b.reportFlush(errDown, 1, start.Add(failureLogEvery))
	if !b.lastFailureLog.Equal(start.Add(failureLogEvery)) {
		t.Fatal("failure after failureLogEvery should log again")
	}
	if b.failedFlushes != 3 || b.droppedTraces != 6 {
		t.Fatalf("failed=%d dropped=%d, want 3 and 6", b.failedFlushes, b.droppedTraces)
	}

	b.reportFlush(nil, 4, start.Add(2*failureLogEvery))
	if b.failedFlushes != 0 || b.droppedTraces != 0 || !b.lastFailureLog.IsZero() {
		t.Fatalf("success must reset counters: failed=%d dropped=%d last=%v",
			b.failedFlushes, b.droppedTraces, b.lastFailureLog)
	}
}
