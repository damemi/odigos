package engine

import (
	"context"
	"time"
)

const (
	// shutdownFlushTimeout bounds the final drain on graceful shutdown so a
	// slow/unreachable DB can't hang termination.
	shutdownFlushTimeout = 10 * time.Second
)

// RunBaselineFlusher runs the flush loop until ctx is cancelled, then does one
// final drain so learning isn't lost on shutdown. The interval is read from
// system settings and applied live (ticker.Reset) when the operator changes it.
// Run in its own goroutine.
func (e *Engine) RunBaselineFlusher(ctx context.Context) {
	interval := e.system.BaselineFlushInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), shutdownFlushTimeout)
			if err := e.Flush(flushCtx); err != nil {
				e.warn("final baseline flush", err)
			}
			e.sweepPromotions(flushCtx)
			cancel()
			return
		case <-ticker.C:
			if err := e.Flush(ctx); err != nil {
				e.warn("baseline flush", err)
			}
			e.sweepPromotions(ctx)
			if next := e.system.BaselineFlushInterval(); next != interval {
				ticker.Reset(next)
				interval = next
			}
		}
	}
}
