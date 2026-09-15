package engine

import (
	"context"
	"sync"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Sample traces are the few whole traces kept for a person to open later: the
// examples shown next to a baseline, and the evidence behind an anomaly or a
// guardrail violation. They are context for a finding, not the finding itself,
// so storing one must never slow down ingestion. Until now each sample cost the
// ingest goroutine a ClickHouse count plus a single-row insert while the
// gateway waited for the OTLP ack. Now the ingest path reserves a slot in
// memory (sampleCounts) and queues the trace; sampleWriter writes the queue in
// batches from its own goroutine.

const (
	// sampleQueueSize bounds how many samples may wait for the writer. Samples
	// are rare by design; when the queue is full they are dropped, not awaited.
	sampleQueueSize = 1024
	// sampleFlushInterval is how long a queued sample waits at most.
	sampleFlushInterval = time.Second
)

// pendingSample is one queued trace plus the hook that hands its slot back if
// the write never happens, so a later trace can be kept instead.
type pendingSample struct {
	obs     repo.Observation
	release func()
}

// sampleWriter writes queued sample traces to the DB in batches.
type sampleWriter struct {
	repo  repo.Repository
	warn  func(msg string, err error)
	queue chan pendingSample
	// afterInsert runs once per transaction after a batch landed, so the
	// in-memory caps can re-sync with what every replica has stored.
	afterInsert func(ctx context.Context, transactionID int64)
}

func newSampleWriter(r repo.Repository, warn func(string, error)) *sampleWriter {
	return &sampleWriter{repo: r, warn: warn, queue: make(chan pendingSample, sampleQueueSize)}
}

// enqueue hands one sample to the writer without ever blocking the caller. A
// full queue drops the sample and releases its slot.
func (w *sampleWriter) enqueue(obs repo.Observation, release func()) {
	select {
	case w.queue <- pendingSample{obs: obs, release: release}:
	default:
		metrics.IncSamplesDropped()
		w.warn("sample writer queue full, dropping sample", nil)
		release()
	}
}

// run drains the queue on a timer until ctx is cancelled, then once more so
// evidence for a finding raised just before shutdown is not lost.
func (w *sampleWriter) run(ctx context.Context) {
	ticker := time.NewTicker(sampleFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := detachedEvidenceContext(context.Background())
			w.drain(drainCtx)
			cancel()
			return
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

// drain writes everything currently queued as one insert. On failure every
// sample releases its slot so the next qualifying trace is kept instead. Also
// called by Engine.Flush.
func (w *sampleWriter) drain(ctx context.Context) {
	var batch []pendingSample
	for more := true; more; {
		select {
		case p := <-w.queue:
			batch = append(batch, p)
		default:
			more = false
		}
	}
	if len(batch) == 0 {
		return
	}
	obs := make([]repo.Observation, len(batch))
	for i, p := range batch {
		obs[i] = p.obs
	}
	if err := w.repo.InsertObservations(ctx, obs); err != nil {
		w.warn("insert observations", err)
		for _, p := range batch {
			p.release()
		}
		return
	}
	if w.afterInsert == nil {
		return
	}
	seen := map[int64]struct{}{}
	for _, o := range obs {
		if _, ok := seen[o.TransactionID]; ok {
			continue
		}
		seen[o.TransactionID] = struct{}{}
		w.afterInsert(ctx, o.TransactionID)
	}
}

// sampleCounts enforces the sample caps (N examples per transaction, one
// evidence trace per issue) from memory. A transaction's counts are loaded from
// the DB once, the first time it wants to keep a sample, so the caps still hold
// after a restart and against samples another replica stored.
type sampleCounts struct {
	repo repo.Repository
	mu   sync.Mutex
	// byTx is transaction id -> sample reason -> stored or queued traces.
	byTx map[int64]map[string]int
}

func newSampleCounts(r repo.Repository) *sampleCounts {
	return &sampleCounts{repo: r, byTx: map[int64]map[string]int{}}
}

// reserve takes one slot for (transaction, reason) if fewer than max are taken.
func (c *sampleCounts) reserve(ctx context.Context, transactionID int64, reason string, max int) (bool, error) {
	counts, err := c.load(ctx, transactionID)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if counts[reason] >= max {
		return false, nil
	}
	counts[reason]++
	return true, nil
}

// release hands a slot back after a write that did not happen.
func (c *sampleCounts) release(transactionID int64, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if counts, ok := c.byTx[transactionID]; ok && counts[reason] > 0 {
		counts[reason]--
	}
}

// load returns a transaction's count map, querying the DB the first time. The
// query runs outside the lock so a slow DB stalls only this caller; if two
// callers load at once the second result is discarded.
func (c *sampleCounts) load(ctx context.Context, transactionID int64) (map[string]int, error) {
	c.mu.Lock()
	counts, ok := c.byTx[transactionID]
	c.mu.Unlock()
	if ok {
		return counts, nil
	}
	fromDB, err := c.repo.CountObservationsByReason(ctx, transactionID)
	if err != nil {
		return nil, err
	}
	if fromDB == nil {
		fromDB = map[string]int{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if counts, ok := c.byTx[transactionID]; ok {
		return counts, nil
	}
	c.byTx[transactionID] = fromDB
	return fromDB, nil
}

// refresh re-reads one transaction's counts from the DB after a batch of its
// samples landed. Each replica reserves slots against its own memory, so with
// N replicas the cap could be overshot N times over; re-syncing after every
// write bounds the overshoot to what was queued between two writes. Only a
// transaction that is already loaded is refreshed - an unknown one loads on its
// next reserve anyway.
func (c *sampleCounts) refresh(ctx context.Context, transactionID int64) {
	c.mu.Lock()
	_, loaded := c.byTx[transactionID]
	c.mu.Unlock()
	if !loaded {
		return
	}
	fromDB, err := c.repo.CountObservationsByReason(ctx, transactionID)
	if err != nil || fromDB == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, still := c.byTx[transactionID]; !still {
		return
	}
	c.byTx[transactionID] = fromDB
}

// forget drops one transaction's counts after its stored samples were deleted,
// so a relearned transaction (same id) captures fresh evidence.
func (c *sampleCounts) forget(transactionID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byTx, transactionID)
}

// clear drops every transaction's counts after an inventory reset.
func (c *sampleCounts) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byTx = map[int64]map[string]int{}
}

// keepSample keeps one trace for a person to open later unless the cap for that
// reason is reached: reserve a slot, queue the write, and report whether it was
// queued. release (optional) runs if the write never lands so the caller can
// undo its own bookkeeping. The trace is encoded here because a Trace is not
// safe to share with the writer goroutine.
func (e *Engine) keepSample(ctx context.Context, t *Trace, tx repo.Transaction, reason string, max int, release func()) bool {
	// Once the engine decided a trace is worth keeping, it is kept even if the
	// instrumented app has already disconnected.
	ctx, cancel := detachedEvidenceContext(ctx)
	defer cancel()
	ok, err := e.sampleCounts.reserve(ctx, tx.ID, reason, max)
	if err != nil {
		e.warn("count stored samples", err)
		return false
	}
	if !ok {
		return false
	}
	e.samples.enqueue(repo.Observation{
		TraceID:       t.TraceID,
		TransactionID: tx.ID,
		ObservedAt:    e.clock(),
		DurationMs:    t.DurationMs,
		SampleReason:  reason,
		RawOTLP:       t.RawOTLP(),
	}, func() {
		e.sampleCounts.release(tx.ID, reason)
		if release != nil {
			release()
		}
	})
	return true
}
