package interrogation

import (
	"context"
	"sync"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

const (
	defaultFlushInterval  = 5 * time.Second
	defaultFlushThreshold = 500
)

// TraceTransaction is one service invocation to correlate under a TraceId.
type TraceTransaction struct {
	Timestamp     time.Time
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	TransactionID string
	RootSpanName  string
	RootSpanKind  string
	SpanIDs       []string // raw FixedString(8) bytes as Go string
}

// TraceBatch is one TraceId with all transactions seen for it.
type TraceBatch struct {
	TraceID      string // raw FixedString(16) bytes as Go string
	Transactions []TraceTransaction
}

type traceCorrelator interface {
	correlate(ctx context.Context, traces []TraceBatch) error
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

	pending map[string]*TraceBatch

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
	if !b.enabled() || len(batch.TraceID) == 0 || len(batch.Transactions) == 0 {
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

	if b.correlator == nil {
		return
	}
	if err := b.correlator.correlate(ctx, toFlush); err != nil {
		b.logger.Error("interrogation call trie correlation failed", "error", err)
	}
}

func (b *invocationBatcher) merge(batch TraceBatch) {
	if len(batch.TraceID) == 0 || len(batch.Transactions) == 0 {
		return
	}
	existing := b.pending[batch.TraceID]
	if existing == nil {
		txs := make([]TraceTransaction, 0, len(batch.Transactions))
		for i := range batch.Transactions {
			txs = append(txs, copyTransaction(batch.Transactions[i]))
		}
		b.pending[batch.TraceID] = &TraceBatch{
			TraceID:      batch.TraceID,
			Transactions: txs,
		}
		return
	}
	for i := range batch.Transactions {
		existing.Transactions = append(existing.Transactions, copyTransaction(batch.Transactions[i]))
	}
}

func copyTransaction(tx TraceTransaction) TraceTransaction {
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
