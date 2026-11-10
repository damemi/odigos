package odigosgroupbytraceprocessor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"

	"github.com/odigos-io/odigos/collector/processors/odigosgroupbytraceprocessor/internal/metadata"
	"github.com/odigos-io/odigos/collector/processors/odigosgroupbytraceprocessor/internal/metadatatest"
)

var (
	traceA = pcommon.TraceID{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	traceB = pcommon.TraceID{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
)

type testSpan struct {
	trace pcommon.TraceID
	root  bool
	kind  ptrace.SpanKind
}

// newTraces puts each group of spans under its own resource, named after its service.
func newTraces(groups map[string][]testSpan) ptrace.Traces {
	td := ptrace.NewTraces()
	for service, spans := range groups {
		rs := td.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", service)
		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName("scope-" + service)
		for i, s := range spans {
			span := ss.Spans().AppendEmpty()
			span.SetTraceID(s.trace)
			span.SetSpanID(pcommon.SpanID{byte(i + 1)})
			span.SetKind(s.kind)
			if !s.root {
				span.SetParentSpanID(pcommon.SpanID{9})
			}
		}
	}
	return td
}

func TestSplitByTrace(t *testing.T) {
	td := newTraces(map[string][]testSpan{
		"frontend": {{trace: traceA, root: true, kind: ptrace.SpanKindServer}, {trace: traceB}},
		"worker":   {{trace: traceA, kind: ptrace.SpanKindConsumer}, {trace: traceA}},
	})

	batches := splitByTrace(td, createDefaultConfig())

	require.Len(t, batches, 2)
	a := batches[traceA]
	assert.Equal(t, 3, a.spans)
	assert.Equal(t, 3, a.td.SpanCount())
	assert.True(t, a.root)
	assert.True(t, a.async)
	assert.Equal(t, 2, a.td.ResourceSpans().Len())

	b := batches[traceB]
	assert.Equal(t, 1, b.td.SpanCount())
	assert.False(t, b.root)
	assert.False(t, b.async)
	service, _ := b.td.ResourceSpans().At(0).Resource().Attributes().Get("service.name")
	assert.Equal(t, "frontend", service.Str())
	assert.Equal(t, "scope-frontend", b.td.ResourceSpans().At(0).ScopeSpans().At(0).Scope().Name())
}

func TestDeadline(t *testing.T) {
	cfg := createDefaultConfig()
	t0 := time.Unix(1000, 0)

	tests := []struct {
		name        string
		batches     []traceBatch
		at          []time.Duration
		wantAfter   time.Duration
		wantSettled bool
	}{
		{
			name:      "sync without root waits sync_wait",
			batches:   []traceBatch{{spans: 1}},
			at:        []time.Duration{0},
			wantAfter: cfg.SyncWait,
		},
		{
			name:        "sync with root settles",
			batches:     []traceBatch{{spans: 1}, {spans: 1, root: true}},
			at:          []time.Duration{0, time.Second},
			wantAfter:   time.Second + cfg.RootSettle,
			wantSettled: true,
		},
		{
			name:        "spans after root push the settle",
			batches:     []traceBatch{{spans: 1, root: true}, {spans: 1}},
			at:          []time.Duration{0, 2 * time.Second},
			wantAfter:   2*time.Second + cfg.RootSettle,
			wantSettled: true,
		},
		{
			name:      "settle never passes sync_wait",
			batches:   []traceBatch{{spans: 1}, {spans: 1, root: true}},
			at:        []time.Duration{0, cfg.SyncWait - time.Second},
			wantAfter: cfg.SyncWait,
		},
		{
			name:      "async waits async_wait even with root",
			batches:   []traceBatch{{spans: 1, root: true}, {spans: 1, async: true}},
			at:        []time.Duration{0, time.Second},
			wantAfter: cfg.AsyncWait,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newShard(10)
			for i, b := range tt.batches {
				b.id = traceA
				b.td = ptrace.NewTraces()
				s.add(&b, t0.Add(tt.at[i]), cfg, 10)
			}

			e := s.traces[traceA]
			assert.Equal(t, t0.Add(tt.wantAfter), e.deadline)
			assert.Equal(t, tt.wantSettled, e.settled)
			assert.Empty(t, s.takeExpired(e.deadline.Add(-time.Millisecond)))
			assert.Len(t, s.takeExpired(e.deadline), 1)
		})
	}
}

func TestEvictionReleasesOldest(t *testing.T) {
	s := newShard(10)
	now := time.Unix(1000, 0)

	_, evicted, _ := s.add(&traceBatch{id: traceA, td: ptrace.NewTraces(), spans: 1}, now, createDefaultConfig(), 1)
	require.Nil(t, evicted)
	created, evicted, _ := s.add(&traceBatch{id: traceB, td: ptrace.NewTraces(), spans: 1}, now, createDefaultConfig(), 1)

	assert.True(t, created)
	require.NotNil(t, evicted)
	assert.Equal(t, traceA, evicted.id)
	assert.Len(t, s.traces, 1)
	assert.Equal(t, 1, s.order.Len())
}

func TestLateSpans(t *testing.T) {
	s := newShard(10)
	cfg := createDefaultConfig()
	t0 := time.Unix(1000, 0)

	s.add(&traceBatch{id: traceA, td: ptrace.NewTraces(), spans: 1, root: true}, t0, cfg, 10)
	require.Len(t, s.takeExpired(t0.Add(cfg.RootSettle)), 1)

	_, _, late := s.add(&traceBatch{id: traceA, td: ptrace.NewTraces(), spans: 2}, t0.Add(5*time.Second), cfg, 10)
	require.NotNil(t, late)
	assert.Equal(t, reasonSettled, late.reason)
	assert.False(t, late.async)

	_, _, late = s.add(&traceBatch{id: traceB, td: ptrace.NewTraces(), spans: 1}, t0.Add(5*time.Second), cfg, 10)
	assert.Nil(t, late)

	// The late spans form a rootless fragment, released at sync_wait.
	fragmentReleased := t0.Add(5*time.Second + cfg.SyncWait)
	require.Len(t, s.takeExpired(fragmentReleased), 2)
	assert.Equal(t, reasonTimeout, s.released[traceA].reason)

	// Past lateSpanWindow the released IDs are forgotten.
	s.takeExpired(fragmentReleased.Add(lateSpanWindow))
	assert.Empty(t, s.released)
	assert.Empty(t, s.releasedQueue)
}

func TestReleasedIDsAreCapped(t *testing.T) {
	s := newShard(2)
	now := time.Unix(1000, 0)
	traceC := pcommon.TraceID{3}

	s.markReleased(&traceEntry{id: traceA}, reasonSettled, now)
	s.markReleased(&traceEntry{id: traceB}, reasonSettled, now)
	// Releasing A again (its late fragment) must survive popping A's older queue slot.
	s.markReleased(&traceEntry{id: traceA}, reasonTimeout, now.Add(time.Second))
	s.markReleased(&traceEntry{id: traceC}, reasonSettled, now.Add(time.Second))

	assert.Len(t, s.releasedQueue, 2)
	assert.Len(t, s.released, 2)
	assert.NotContains(t, s.released, traceB)
	assert.Equal(t, reasonTimeout, s.released[traceA].reason)
	assert.Contains(t, s.released, traceC)
}

func TestSplitByTraceCountsLongSpans(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.AsyncWait = time.Minute
	td := ptrace.NewTraces()
	spans := td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans()
	for _, d := range []time.Duration{time.Second, 45 * time.Second, 2 * time.Minute} {
		span := spans.AppendEmpty()
		span.SetTraceID(traceA)
		span.SetStartTimestamp(pcommon.Timestamp(time.Second))
		span.SetEndTimestamp(pcommon.Timestamp(time.Second + d))
	}

	b := splitByTrace(td, cfg)[traceA]

	assert.Equal(t, 2, b.longSync)
	assert.Equal(t, 1, b.longAsync)
}

func TestProcessorReleasesWholeTraces(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.RootSettle = 50 * time.Millisecond
	sink := new(consumertest.TracesSink)
	p, err := newGroupByTraceProcessor(processortest.NewNopSettings(metadata.Type), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, p.Start(context.Background(), componenttest.NewNopHost()))

	require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{
		"backend": {{trace: traceA}, {trace: traceB, kind: ptrace.SpanKindProducer}},
	})))
	require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{
		"frontend": {{trace: traceA, root: true}},
	})))

	require.Eventually(t, func() bool { return len(sink.AllTraces()) == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 2, sink.AllTraces()[0].SpanCount())

	// The async trace is still within async_wait, so only shutdown releases it.
	require.NoError(t, p.Shutdown(context.Background()))
	require.Len(t, sink.AllTraces(), 2)
	assert.Equal(t, 1, sink.AllTraces()[1].SpanCount())
}

func TestMemoryPressureRejects(t *testing.T) {
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	sink := new(consumertest.TracesSink)
	p, err := newGroupByTraceProcessor(metadatatest.NewSettings(tt), createDefaultConfig(), sink)
	require.NoError(t, err)
	p.memLimitReached = func() bool { return true }

	err = p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{
		"frontend": {{trace: traceA, root: true}, {trace: traceB}},
	}))

	require.ErrorIs(t, err, errMemoryLimitReached)
	assert.Empty(t, p.shardFor(traceA).traces)
	metadatatest.AssertEqualOdigosMemoryPressureSpans(t, tt, []metricdata.DataPoint[int64]{{
		Value: 2,
		Attributes: attribute.NewSet(
			attribute.String("component", metadata.Type.String()), attribute.String("action", "rejected")),
	}}, metricdatatest.IgnoreTimestamp())
}

func TestLateLongRunningSpans(t *testing.T) {
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	cfg := createDefaultConfig()
	cfg.RootSettle = 50 * time.Millisecond
	sink := new(consumertest.TracesSink)
	p, err := newGroupByTraceProcessor(metadatatest.NewSettings(tt), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, p.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { require.NoError(t, p.Shutdown(context.Background())) })

	require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{
		"frontend": {{trace: traceA}},
	})))
	// The child is settled once the root shows up; the root alone keeps the trace waiting.
	require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{
		"backend": {{trace: traceA, root: true}},
	})))
	require.Eventually(t, func() bool { return len(sink.AllTraces()) == 1 }, 2*time.Second, 10*time.Millisecond)

	// A watch's span outlasting sync_wait arrives with a regular late span.
	late := newTraces(map[string][]testSpan{"api": {{trace: traceA}, {trace: traceA}}})
	watch := late.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	watch.SetStartTimestamp(pcommon.Timestamp(time.Second))
	watch.SetEndTimestamp(pcommon.Timestamp(time.Second + 2*cfg.SyncWait))
	require.NoError(t, p.ConsumeTraces(context.Background(), late))

	metadatatest.AssertEqualOdigosGroupbytraceLateSpans(t, tt, []metricdata.DataPoint[int64]{
		{Value: 1, Attributes: attribute.NewSet(attribute.String("reason", "long_running"), attribute.String("class", "sync"))},
		{Value: 1, Attributes: attribute.NewSet(attribute.String("reason", "settled"), attribute.String("class", "sync"))},
	}, metricdatatest.IgnoreTimestamp())
	metadatatest.AssertEqualOdigosGroupbytraceLateTraces(t, tt, []metricdata.DataPoint[int64]{
		{Value: 1, Attributes: attribute.NewSet(attribute.String("reason", "long_running"), attribute.String("class", "sync"))},
	}, metricdatatest.IgnoreTimestamp())
}

func TestLateTracesCountedOnce(t *testing.T) {
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	cfg := createDefaultConfig()
	cfg.RootSettle = 50 * time.Millisecond
	cfg.SyncWait = 200 * time.Millisecond
	sink := new(consumertest.TracesSink)
	p, err := newGroupByTraceProcessor(metadatatest.NewSettings(tt), cfg, sink)
	require.NoError(t, err)
	require.NoError(t, p.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { require.NoError(t, p.Shutdown(context.Background())) })

	require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{
		"frontend": {{trace: traceA, root: true}},
	})))
	require.Eventually(t, func() bool { return len(sink.AllTraces()) == 1 }, 2*time.Second, 10*time.Millisecond)

	// Two late batches, then another one after the late fragment itself was released.
	for range 2 {
		require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{"api": {{trace: traceA}}})))
	}
	require.Eventually(t, func() bool { return len(sink.AllTraces()) == 2 }, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, p.ConsumeTraces(context.Background(), newTraces(map[string][]testSpan{"api": {{trace: traceA}}})))

	metadatatest.AssertEqualOdigosGroupbytraceLateSpans(t, tt, []metricdata.DataPoint[int64]{
		{Value: 2, Attributes: attribute.NewSet(attribute.String("reason", "settled"), attribute.String("class", "sync"))},
		{Value: 1, Attributes: attribute.NewSet(attribute.String("reason", "timeout"), attribute.String("class", "sync"))},
	}, metricdatatest.IgnoreTimestamp())
	metadatatest.AssertEqualOdigosGroupbytraceLateTraces(t, tt, []metricdata.DataPoint[int64]{
		{Value: 1, Attributes: attribute.NewSet(attribute.String("reason", "settled"), attribute.String("class", "sync"))},
	}, metricdatatest.IgnoreTimestamp())
}
