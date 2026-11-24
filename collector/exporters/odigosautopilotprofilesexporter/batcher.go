package odigosautopilotprofilesexporter

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/odigos-io/odigos/collector/exporters/odigosautopilotprofilesexporter/internal/metadata"
)

// stackWriter persists batched profile stacks to Redis.
type stackWriter interface {
	insertStacks(ctx context.Context, stacks []profileStack) error
	Close() error
}

// stackBatcher buffers immutable profile stacks on a single goroutine and
// flushes them through stackWriter. recordStack is non-blocking; a full
// channel drops the stack.
type stackBatcher struct {
	ch             chan profileStack
	flushInterval  time.Duration
	flushThreshold int
	logger         *zap.Logger
	// throttledLogger logs each message at most once per failureLogEvery, so a
	// slow or unreachable Redis does not log every flush or drop.
	throttledLogger *zap.Logger
	// telemetry counts dropped stacks; nil when the builder failed to initialize.
	telemetry *metadata.TelemetryBuilder
	writer    stackWriter

	pending []profileStack

	// failing is set while flushes fail; owned by the loop goroutine.
	failing bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newStackBatcher(flushInterval time.Duration, flushThreshold int, logger *zap.Logger, telemetry *metadata.TelemetryBuilder) *stackBatcher {
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}
	if flushThreshold <= 0 {
		flushThreshold = defaultFlushThreshold
	}
	return &stackBatcher{
		ch:             make(chan profileStack, defaultFlushThreshold),
		flushInterval:  flushInterval,
		flushThreshold: flushThreshold,
		logger:         logger,
		throttledLogger: logger.WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core {
			return zapcore.NewSamplerWithOptions(c, failureLogEvery, 1, 0)
		})),
		telemetry: telemetry,
		pending:   make([]profileStack, 0, flushThreshold),
	}
}

func (b *stackBatcher) start(writer stackWriter) {
	b.writer = writer
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.wg.Add(1)
	go b.loop(ctx)
}

func (b *stackBatcher) stop() error {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
		b.wg.Wait()
	}
	if b.writer != nil {
		err := b.writer.Close()
		b.writer = nil
		return err
	}
	return nil
}

func (b *stackBatcher) enabled() bool {
	return b != nil && b.cancel != nil
}

func (b *stackBatcher) recordStack(ctx context.Context, s profileStack) {
	if !b.enabled() {
		return
	}
	select {
	case b.ch <- s:
	default:
		if b.telemetry != nil {
			b.telemetry.OdigosAutopilotProfilesExporterQueueDroppedStacks.Add(ctx, 1)
		}
		b.throttledLogger.Warn("autopilot profiles redis is too slow to keep up; skipping some stacks, call trees fill in from later profiles",
			zap.Int("queue_size", cap(b.ch)),
		)
	}
}

// flush writes pending and blocks this goroutine on insertStacks, so the
// channel is not read until the Redis batch finishes.
func (b *stackBatcher) flush(ctx context.Context) {
	if len(b.pending) == 0 {
		return
	}
	toFlush := b.pending
	b.pending = make([]profileStack, 0, b.flushThreshold)

	if b.writer == nil {
		return
	}
	start := time.Now()
	err := b.writer.insertStacks(ctx, toFlush)
	if b.telemetry != nil {
		b.telemetry.OdigosAutopilotProfilesExporterCacheWriteDuration.Record(ctx, time.Since(start).Seconds())
	}
	if err != nil {
		b.failing = true
		if b.telemetry != nil {
			b.telemetry.OdigosAutopilotProfilesExporterCacheWriteFailedStacks.Add(ctx, int64(len(toFlush)))
		}
		b.throttledLogger.Error("autopilot profiles redis insert failed; dropping stacks", zap.Error(err))
		return
	}
	if b.telemetry != nil {
		b.telemetry.OdigosAutopilotProfilesExporterCacheWrittenStacks.Add(ctx, int64(len(toFlush)))
	}
	if b.failing {
		b.failing = false
		b.logger.Info("autopilot profiles redis insert recovered")
	}
}

func (b *stackBatcher) loop(ctx context.Context) {
	defer b.wg.Done()

	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drop pending and queued stacks instead of a last Redis write.
			return
		case <-ticker.C:
			b.flush(ctx)
		case s := <-b.ch:
			if len(s.TraceID) == 0 || len(s.SpanID) == 0 || len(s.Frames) == 0 {
				continue
			}
			b.pending = append(b.pending, s)
			if len(b.pending) >= b.flushThreshold {
				b.flush(ctx)
			}
		}
	}
}
