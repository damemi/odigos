package odigosurltemplatizationexporter

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type unmatchedPathInc struct {
	workload string
	kind     ptrace.SpanKind
	path     string
}

// pathBatcher merges unmatched paths on a single goroutine and flushes them
// through pathCounter. recordPath is non-blocking; a full channel drops the path.
type pathBatcher struct {
	ch             chan unmatchedPathInc
	flushInterval  time.Duration
	flushThreshold int
	logger         *zap.Logger
	counter        pathCounter

	pending  map[string]map[string]int64
	pendingN int

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newPathBatcher(flushInterval time.Duration, flushThreshold int, logger *zap.Logger) *pathBatcher {
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}
	return &pathBatcher{
		ch:             make(chan unmatchedPathInc, defaultFlushThreshold),
		flushInterval:  flushInterval,
		flushThreshold: flushThreshold,
		logger:         logger,
		pending:        make(map[string]map[string]int64),
	}
}

func (b *pathBatcher) start(counter pathCounter) {
	b.counter = counter
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.wg.Add(1)
	go b.loop(ctx)
}

func (b *pathBatcher) stop() error {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
		b.wg.Wait()
	}
	if b.counter != nil {
		err := b.counter.Close()
		b.counter = nil
		return err
	}
	return nil
}

func (b *pathBatcher) recordPath(workload string, kind ptrace.SpanKind, path string) {
	select {
	case b.ch <- unmatchedPathInc{workload: workload, kind: kind, path: path}:
	default:
	}
}

func (b *pathBatcher) merge(inc unmatchedPathInc) {
	key := unmatchedRedisKey(inc.workload, inc.kind)
	if key == "" {
		return
	}
	byPath := b.pending[key]
	if byPath == nil {
		byPath = make(map[string]int64)
		b.pending[key] = byPath
	}
	if _, exists := byPath[inc.path]; !exists {
		b.pendingN++
	}
	byPath[inc.path]++
}

// flush writes pending and blocks this goroutine on incrPaths, so the
// channel is not read until the write finishes. ctx cancel/timeout aborts the Redis write.
func (b *pathBatcher) flush(ctx context.Context) {
	if b.pendingN == 0 {
		return
	}
	toFlush := b.pending
	b.pending = make(map[string]map[string]int64)
	b.pendingN = 0

	if b.counter == nil {
		return
	}
	if err := b.counter.incrPaths(ctx, toFlush); err != nil {
		b.logger.Error("failed to write unmatched path counts to cache", zap.Error(err))
	}
}

func (b *pathBatcher) loop(ctx context.Context) {
	defer b.wg.Done()

	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Samples only: drop pending and queued paths instead of a last Redis write.
			return
		case <-ticker.C:
			b.flush(ctx)
		case inc := <-b.ch:
			b.merge(inc)
			if b.pendingN >= b.flushThreshold {
				b.flush(ctx)
			}
		}
	}
}
