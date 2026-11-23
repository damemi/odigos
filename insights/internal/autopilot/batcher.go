package autopilot

import (
	"context"
	"sync"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
)

const (
	defaultFlushInterval  = 1 * time.Second
	defaultFlushThreshold = 500
	failureLogEvery       = time.Minute
)

// ServiceInvocation is one transaction's service invocation to correlate under
// a trace id: its entry span and the spans of its own invocation.
type ServiceInvocation struct {
	// SourceIdentity is the transaction's k8s source identity (resource
	// attributes); the call tree key's source id is its repo.HashSourceID.
	SourceIdentity map[string]string
	TransactionID  string
	SpanIDs        []string // hex span ids
	// Measures are the timings of the invocation's probe and entry spans.
	Measures []measure.Sample
}

// TraceBatch is one trace id with all service invocations seen for it.
type TraceBatch struct {
	TraceID            string // hex trace id
	ServiceInvocations []ServiceInvocation
}

type traceCorrelator interface {
	correlate(ctx context.Context, traces []TraceBatch) error
	mergeMeasures(ctx context.Context, set measureSet) error
	Close() error
}

// invocationBatcher buffers trace batches on a single goroutine and flushes
// them through traceCorrelator. Pending entries are merged by TraceId so each
// Redis fetch runs once per trace. record is non-blocking; a full channel drops.
type invocationBatcher struct {
	ch             chan TraceBatch
	flushInterval  time.Duration
	flushThreshold int
	logger         *commonlogger.OdigosLogger
	correlator     traceCorrelator

	pending  map[string]*TraceBatch
	measures measureSet

	// Flush-failure log throttling; owned by the loop goroutine.
	failedFlushes  int
	droppedTraces  int
	lastFailureLog time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newInvocationBatcher(flushInterval time.Duration, flushThreshold int, logger *commonlogger.OdigosLogger) *invocationBatcher {
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}
	if flushThreshold <= 0 {
		flushThreshold = defaultFlushThreshold
	}
	return &invocationBatcher{
		ch:             make(chan TraceBatch, defaultFlushThreshold),
		flushInterval:  flushInterval,
		flushThreshold: flushThreshold,
		logger:         logger,
		pending:        make(map[string]*TraceBatch, flushThreshold),
		measures:       measureSet{},
	}
}

func (b *invocationBatcher) start(correlator traceCorrelator) {
	b.correlator = correlator
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.wg.Add(1)
	go b.loop(ctx)
}

func (b *invocationBatcher) stop() error {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
		b.wg.Wait()
	}
	if b.correlator != nil {
		err := b.correlator.Close()
		b.correlator = nil
		return err
	}
	return nil
}

func (b *invocationBatcher) enabled() bool {
	return b != nil && b.cancel != nil
}

func (b *invocationBatcher) record(batch TraceBatch) {
	if !b.enabled() || len(batch.TraceID) == 0 || len(batch.ServiceInvocations) == 0 {
		return
	}
	select {
	case b.ch <- batch:
	default:
	}
}

func (b *invocationBatcher) flush(ctx context.Context) {
	if len(b.pending) == 0 {
		return
	}
	toFlush := make([]TraceBatch, 0, len(b.pending))
	for _, tb := range b.pending {
		toFlush = append(toFlush, *tb)
	}
	b.pending = make(map[string]*TraceBatch, b.flushThreshold)
	set := b.measures
	b.measures = measureSet{}

	if b.correlator == nil {
		return
	}
	// Measurements are best effort: a failure is logged and the set dropped.
	if err := b.correlator.mergeMeasures(ctx, set); err != nil {
		b.logger.Warn("autopilot probe measurements dropped", "error", err)
	}
	err := b.correlator.correlate(ctx, toFlush)
	b.reportFlush(err, len(toFlush), time.Now())
}

// reportFlush logs correlation failures at most once per failureLogEvery so
// a Redis outage (or one still starting up) does not log every flush, and
// logs once when flushes recover. The dropped batches are lost; later traces
// correlate normally once Redis is reachable.
func (b *invocationBatcher) reportFlush(err error, traces int, now time.Time) {
	if err == nil {
		if b.failedFlushes > 0 {
			b.logger.Info("autopilot call tree correlation recovered",
				"failed_flushes", b.failedFlushes, "dropped_traces", b.droppedTraces)
			b.failedFlushes, b.droppedTraces = 0, 0
			b.lastFailureLog = time.Time{}
		}
		return
	}
	b.failedFlushes++
	b.droppedTraces += traces
	if b.lastFailureLog.IsZero() || now.Sub(b.lastFailureLog) >= failureLogEvery {
		b.logger.Error("autopilot call tree correlation failed; will retry on next flush",
			"error", err, "failed_flushes", b.failedFlushes, "dropped_traces", b.droppedTraces)
		b.lastFailureLog = now
	}
}

func (b *invocationBatcher) merge(batch TraceBatch) {
	if len(batch.TraceID) == 0 || len(batch.ServiceInvocations) == 0 {
		return
	}
	for i := range batch.ServiceInvocations {
		b.measures.add(batch.ServiceInvocations[i])
	}
	existing := b.pending[batch.TraceID]
	if existing == nil {
		txs := make([]ServiceInvocation, 0, len(batch.ServiceInvocations))
		for i := range batch.ServiceInvocations {
			txs = append(txs, copyServiceInvocation(batch.ServiceInvocations[i]))
		}
		b.pending[batch.TraceID] = &TraceBatch{
			TraceID:            batch.TraceID,
			ServiceInvocations: txs,
		}
		return
	}
	for i := range batch.ServiceInvocations {
		existing.ServiceInvocations = append(existing.ServiceInvocations, copyServiceInvocation(batch.ServiceInvocations[i]))
	}
}

func copyServiceInvocation(tx ServiceInvocation) ServiceInvocation {
	cp := tx
	cp.SpanIDs = append([]string(nil), tx.SpanIDs...)
	return cp
}

func (b *invocationBatcher) loop(ctx context.Context) {
	defer b.wg.Done()

	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.flush(ctx)
		case batch := <-b.ch:
			b.merge(batch)
			if len(b.pending) >= b.flushThreshold {
				b.flush(ctx)
			}
		}
	}
}
