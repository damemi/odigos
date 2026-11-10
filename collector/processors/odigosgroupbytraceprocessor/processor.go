package odigosgroupbytraceprocessor

import (
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"
	"time"

	rtml "github.com/odigos-io/go-rtml"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"

	"github.com/odigos-io/odigos/collector/processors/odigosgroupbytraceprocessor/internal/metadata"
)

const (
	releaseTick = 100 * time.Millisecond
	// lateSpanWindow is how long a released trace ID is remembered to count spans
	// that arrive after it.
	lateSpanWindow = time.Minute
	// releasedIDBytes is the measured memory per remembered trace ID: its released map
	// entry plus its releasedQueue slot, both with growth slack.
	releasedIDBytes = 135
)

type releaseReason uint8

const (
	reasonSettled releaseReason = iota
	reasonTimeout
	reasonEvicted
	reasonShutdown
	// reasonLongRunning is only used for late spans longer than the wait they missed.
	reasonLongRunning
)

var reasonNames = [...]string{"settled", "timeout", "evicted", "shutdown", "long_running"}

func (r releaseReason) String() string { return reasonNames[r] }

func className(async bool) string {
	if async {
		return "async"
	}
	return "sync"
}

// Not permanent, so the OTLP receiver answers Unavailable and the sender retries.
var errMemoryLimitReached = errors.New("odigosgroupbytrace: memory limit reached, retry later")

// traceBatch is the part of one incoming request that belongs to a single trace.
type traceBatch struct {
	id    pcommon.TraceID
	td    ptrace.Traces
	spans int
	async bool
	root  bool
	// longSync and longAsync count spans whose own duration exceeds sync_wait and
	// async_wait, e.g. a watch or stream root, which no wait increase would catch.
	longSync  int
	longAsync int
}

type traceEntry struct {
	id        pcommon.TraceID
	td        ptrace.Traces
	spans     int
	firstSeen time.Time
	lastSeen  time.Time
	deadline  time.Time
	async     bool
	rootSeen  bool
	// settled is set when the deadline comes from root_settle rather than the class wait.
	settled bool
	order   *list.Element
}

func (e *traceEntry) updateDeadline(cfg *Config) {
	e.settled = false
	if e.async {
		e.deadline = e.firstSeen.Add(cfg.AsyncWait)
		return
	}
	e.deadline = e.firstSeen.Add(cfg.SyncWait)
	if e.rootSeen && cfg.RootSettle > 0 {
		if settle := e.lastSeen.Add(cfg.RootSettle); settle.Before(e.deadline) {
			e.deadline = settle
			e.settled = true
		}
	}
}

func (e *traceEntry) expiryReason() releaseReason {
	if e.settled {
		return reasonSettled
	}
	return reasonTimeout
}

// releasedTrace remembers how a trace was released, to attribute its late spans.
type releasedTrace struct {
	until  int64
	reason releaseReason
	async  bool
	// lateCounted is set once the trace is counted in late_traces, and kept when its
	// late fragment is released again, so a trace is counted once however many pieces it has.
	lateCounted bool
}

type releasedID struct {
	id    pcommon.TraceID
	until int64
}

type shard struct {
	mu     sync.Mutex
	traces map[pcommon.TraceID]*traceEntry
	// order holds the entries oldest first, so eviction is O(1).
	order    *list.List
	released map[pcommon.TraceID]releasedTrace
	// releasedQueue holds released IDs oldest first. Every ID is kept for the same
	// lateSpanWindow, so expiry and the maxReleased cap only ever pop the front.
	releasedQueue []releasedID
	maxReleased   int
}

func newShard(maxReleased int) *shard {
	return &shard{
		traces:      map[pcommon.TraceID]*traceEntry{},
		order:       list.New(),
		released:    map[pcommon.TraceID]releasedTrace{},
		maxReleased: maxReleased,
	}
}

// add buffers b. late is set when b's trace was already released within lateSpanWindow.
func (s *shard) add(b *traceBatch, now time.Time, cfg *Config, maxTraces int) (created bool, evicted *traceEntry, late *releasedTrace) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r, ok := s.released[b.id]; ok && now.UnixNano() < r.until {
		seen := r
		late = &seen
		if !r.lateCounted {
			r.lateCounted = true
			s.released[b.id] = r
		}
	}
	e, ok := s.traces[b.id]
	if ok {
		b.td.ResourceSpans().MoveAndAppendTo(e.td.ResourceSpans())
	} else {
		if len(s.traces) >= maxTraces {
			evicted = s.order.Front().Value.(*traceEntry)
			s.remove(evicted)
			s.markReleased(evicted, reasonEvicted, now)
		}
		e = &traceEntry{id: b.id, td: b.td, firstSeen: now}
		e.order = s.order.PushBack(e)
		s.traces[b.id] = e
		created = true
	}
	e.spans += b.spans
	e.lastSeen = now
	e.async = e.async || b.async
	e.rootSeen = e.rootSeen || b.root
	e.updateDeadline(cfg)
	return created, evicted, late
}

func (s *shard) takeExpired(now time.Time) []*traceEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	for len(s.releasedQueue) > 0 && s.releasedQueue[0].until <= now.UnixNano() {
		s.forgetOldestReleased()
	}
	var expired []*traceEntry
	for _, e := range s.traces {
		if !now.Before(e.deadline) {
			expired = append(expired, e)
		}
	}
	for _, e := range expired {
		s.remove(e)
		s.markReleased(e, e.expiryReason(), now)
	}
	return expired
}

func (s *shard) markReleased(e *traceEntry, reason releaseReason, now time.Time) {
	if s.maxReleased == 0 {
		return
	}
	if len(s.releasedQueue) >= s.maxReleased {
		s.forgetOldestReleased()
	}
	until := now.Add(lateSpanWindow).UnixNano()
	s.released[e.id] = releasedTrace{until: until, reason: reason, async: e.async, lateCounted: s.released[e.id].lateCounted}
	s.releasedQueue = append(s.releasedQueue, releasedID{id: e.id, until: until})
}

func (s *shard) forgetOldestReleased() {
	oldest := s.releasedQueue[0]
	s.releasedQueue = s.releasedQueue[1:]
	// A trace released again (its late fragment) has a newer entry that must stay.
	if r, ok := s.released[oldest.id]; ok && r.until == oldest.until {
		delete(s.released, oldest.id)
	}
}

func (s *shard) takeAll() []*traceEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	all := make([]*traceEntry, 0, len(s.traces))
	for _, e := range s.traces {
		all = append(all, e)
	}
	clear(s.traces)
	s.order.Init()
	return all
}

func (s *shard) remove(e *traceEntry) {
	delete(s.traces, e.id)
	s.order.Remove(e.order)
}

type groupByTraceProcessor struct {
	cfg         *Config
	next        consumer.Traces
	logger      *zap.Logger
	telemetry   *metadata.TelemetryBuilder
	shards      []*shard
	maxPerShard int
	// memLimitReached is rtml.IsMemLimitReached, replaceable in tests.
	memLimitReached func() bool
	rejectedAttrs   metric.MeasurementOption

	stop chan struct{}
	wg   sync.WaitGroup
}

var _ processor.Traces = (*groupByTraceProcessor)(nil)

func newGroupByTraceProcessor(set processor.Settings, cfg *Config, next consumer.Traces) (*groupByTraceProcessor, error) {
	telemetry, err := metadata.NewTelemetryBuilder(set.TelemetrySettings)
	if err != nil {
		return nil, err
	}
	// One shard per usable CPU; fewer shards than GOMAXPROCS fell behind under high load.
	numShards := runtime.GOMAXPROCS(0)
	p := &groupByTraceProcessor{
		cfg:         cfg,
		next:        next,
		logger:      set.Logger,
		telemetry:   telemetry,
		shards:      make([]*shard, numShards),
		maxPerShard: max(1, (cfg.MaxTraces+numShards-1)/numShards),

		memLimitReached: rtml.IsMemLimitReached,
		rejectedAttrs: metric.WithAttributes(
			attribute.String("component", set.ID.String()), attribute.String("action", "rejected")),
	}
	maxReleasedPerShard := cfg.LateSpansMaxMemoryMiB << 20 / releasedIDBytes / numShards
	for i := range p.shards {
		p.shards[i] = newShard(maxReleasedPerShard)
	}
	return p, nil
}

func (*groupByTraceProcessor) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: true}
}

func (p *groupByTraceProcessor) Start(context.Context, component.Host) error {
	p.stop = make(chan struct{})
	for _, s := range p.shards {
		p.wg.Go(func() { p.releaseLoop(s) })
	}
	return nil
}

func (p *groupByTraceProcessor) Shutdown(context.Context) error {
	if p.stop != nil {
		close(p.stop)
		p.wg.Wait()
	}
	for _, s := range p.shards {
		for _, e := range s.takeAll() {
			p.release(e, reasonShutdown)
		}
	}
	p.telemetry.Shutdown()
	return nil
}

func (p *groupByTraceProcessor) ConsumeTraces(_ context.Context, td ptrace.Traces) error {
	ctx := context.Background()
	if p.memLimitReached() {
		p.telemetry.OdigosMemoryPressureSpans.Add(ctx, int64(td.SpanCount()), p.rejectedAttrs)
		return errMemoryLimitReached
	}
	now := time.Now()
	for _, b := range splitByTrace(td, p.cfg) {
		created, evicted, late := p.shardFor(b.id).add(b, now, p.cfg, p.maxPerShard)
		if created {
			p.telemetry.OdigosGroupbytraceTracesInMemory.Add(ctx, 1)
		}
		p.telemetry.OdigosGroupbytraceSpansInMemory.Add(ctx, int64(b.spans))
		if late != nil {
			long := b.longSync
			if late.async {
				long = b.longAsync
			}
			p.addLateSpans(ctx, long, reasonLongRunning, late.async)
			p.addLateSpans(ctx, b.spans-long, late.reason, late.async)
			if !late.lateCounted {
				reason := late.reason
				if long > 0 {
					reason = reasonLongRunning
				}
				p.telemetry.OdigosGroupbytraceLateTraces.Add(ctx, 1, metric.WithAttributes(
					attribute.String("reason", reason.String()), attribute.String("class", className(late.async))))
			}
		}
		if evicted != nil {
			p.release(evicted, reasonEvicted)
		}
	}
	return nil
}

func (p *groupByTraceProcessor) addLateSpans(ctx context.Context, n int, reason releaseReason, async bool) {
	if n == 0 {
		return
	}
	p.telemetry.OdigosGroupbytraceLateSpans.Add(ctx, int64(n), metric.WithAttributes(
		attribute.String("reason", reason.String()), attribute.String("class", className(async))))
}

func (p *groupByTraceProcessor) shardFor(id pcommon.TraceID) *shard {
	return p.shards[binary.BigEndian.Uint64(id[8:])%uint64(len(p.shards))]
}

// releaseLoop releases a shard's expired traces one at a time, so a slow next
// consumer holds traces in the bounded buffer instead of piling up goroutines.
func (p *groupByTraceProcessor) releaseLoop(s *shard) {
	ticker := time.NewTicker(releaseTick)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case now := <-ticker.C:
			for _, e := range s.takeExpired(now) {
				p.release(e, e.expiryReason())
			}
		}
	}
}

func (p *groupByTraceProcessor) release(e *traceEntry, reason releaseReason) {
	ctx := context.Background()
	attrs := metric.WithAttributes(attribute.String("reason", reason.String()), attribute.String("class", className(e.async)))
	p.telemetry.OdigosGroupbytraceTracesInMemory.Add(ctx, -1)
	p.telemetry.OdigosGroupbytraceSpansInMemory.Add(ctx, -int64(e.spans))
	p.telemetry.OdigosGroupbytraceTracesReleased.Add(ctx, 1, attrs)
	p.telemetry.OdigosGroupbytraceSpansReleased.Add(ctx, int64(e.spans), attrs)
	// Exporters report their own failures; logging here would repeat every one of them.
	if err := p.next.ConsumeTraces(ctx, e.td); err != nil {
		p.logger.Debug("next consumer rejected trace", zap.Stringer("trace_id", e.id), zap.Error(err))
	}
}

// splitByTrace moves td's spans into one batch per trace, keeping each span under a
// copy of its resource and scope. Spans are moved, not copied (unlike contrib's
// batchpersignal.SplitTraces), since the processor owns td (MutatesData).
func splitByTrace(td ptrace.Traces, cfg *Config) map[pcommon.TraceID]*traceBatch {
	batches := map[pcommon.TraceID]*traceBatch{}
	rsByTrace := map[pcommon.TraceID]ptrace.ResourceSpans{}
	ssByTrace := map[pcommon.TraceID]ptrace.SpanSlice{}
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		clear(rsByTrace)
		sss := rs.ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			ss := sss.At(j)
			clear(ssByTrace)
			spans := ss.Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				id := span.TraceID()
				b := batches[id]
				if b == nil {
					b = &traceBatch{id: id, td: ptrace.NewTraces()}
					batches[id] = b
				}
				dst, ok := ssByTrace[id]
				if !ok {
					rsOut, ok := rsByTrace[id]
					if !ok {
						rsOut = b.td.ResourceSpans().AppendEmpty()
						rs.Resource().CopyTo(rsOut.Resource())
						rsOut.SetSchemaUrl(rs.SchemaUrl())
						rsByTrace[id] = rsOut
					}
					ssOut := rsOut.ScopeSpans().AppendEmpty()
					ss.Scope().CopyTo(ssOut.Scope())
					ssOut.SetSchemaUrl(ss.SchemaUrl())
					dst = ssOut.Spans()
					ssByTrace[id] = dst
				}
				kind := span.Kind()
				b.async = b.async || kind == ptrace.SpanKindProducer || kind == ptrace.SpanKindConsumer
				b.root = b.root || span.ParentSpanID().IsEmpty()
				b.spans++
				if start, end := span.StartTimestamp(), span.EndTimestamp(); end > start {
					d := time.Duration(end - start)
					if d > cfg.SyncWait {
						b.longSync++
					}
					if d > cfg.AsyncWait {
						b.longAsync++
					}
				}
				span.MoveTo(dst.AppendEmpty())
			}
		}
	}
	return batches
}
