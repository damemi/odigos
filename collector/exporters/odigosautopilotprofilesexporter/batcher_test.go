package odigosautopilotprofilesexporter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/odigos-io/odigos/collector/exporters/odigosautopilotprofilesexporter/internal/metadata"
	"github.com/odigos-io/odigos/collector/exporters/odigosautopilotprofilesexporter/internal/metadatatest"
)

type stubStackWriter struct {
	mu      sync.Mutex
	calls   atomic.Int32
	got     [][]profileStack
	block   chan struct{}
	started chan struct{}
}

func (s *stubStackWriter) insertStacks(_ context.Context, stacks []profileStack) error {
	s.calls.Add(1)
	if s.started != nil {
		select {
		case <-s.started:
		default:
			close(s.started)
		}
	}
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]profileStack, len(stacks))
	copy(cp, stacks)
	s.got = append(s.got, cp)
	return nil
}

func (s *stubStackWriter) Close() error { return nil }

func TestStackBatcher_FlushesOnThreshold(t *testing.T) {
	w := &stubStackWriter{}
	b := newStackBatcher(time.Hour, 2, zap.NewNop(), nil)
	b.start(w)
	t.Cleanup(func() { _ = b.stop() })

	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	b.recordStack(context.Background(), profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "a", FrameType: "jvm"}},
	})
	assert.Equal(t, int32(0), w.calls.Load())

	b.recordStack(context.Background(), profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "b", FrameType: "jvm"}},
	})
	require.Eventually(t, func() bool { return w.calls.Load() == 1 }, time.Second, time.Millisecond)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.got, 1)
	assert.Len(t, w.got[0], 2)
}

func TestStackBatcher_DropsWhenChannelFull(t *testing.T) {
	w := &stubStackWriter{
		started: make(chan struct{}),
		block:   make(chan struct{}),
	}
	core, logs := observer.New(zap.WarnLevel)
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	tb, err := metadata.NewTelemetryBuilder(tt.NewTelemetrySettings())
	require.NoError(t, err)
	b := newStackBatcher(time.Hour, 1, zap.New(core), tb)
	b.start(w)
	t.Cleanup(func() {
		close(w.block)
		_ = b.stop()
	})

	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	b.recordStack(context.Background(), profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "a", FrameType: "jvm"}},
	})
	<-w.started

	for i := 0; i < defaultFlushThreshold+8; i++ {
		b.recordStack(context.Background(), profileStack{
			TraceID: traceID, SpanID: spanID, SampleType: "samples",
			Frames: []profileFrame{{FunctionName: "x", FrameType: "jvm"}},
		})
	}
	assert.Equal(t, int32(1), w.calls.Load(), "batcher must not start another redis write while the first is in flight")
	assert.Equal(t, 1, logs.FilterMessage("autopilot profiles redis is too slow to keep up; skipping some stacks, call trees fill in from later profiles").Len())
	metadatatest.AssertEqualOdigosAutopilotProfilesExporterQueueDroppedStacks(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 8}}, metricdatatest.IgnoreTimestamp())
}

type failingStackWriter struct{ err error }

func (f *failingStackWriter) insertStacks(context.Context, []profileStack) error { return f.err }
func (f *failingStackWriter) Close() error                                       { return nil }

func TestStackBatcher_FlushFailuresLogOnceAndRecover(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	tt := componenttest.NewTelemetry()
	t.Cleanup(func() { require.NoError(t, tt.Shutdown(context.Background())) })
	tb, err := metadata.NewTelemetryBuilder(tt.NewTelemetrySettings())
	require.NoError(t, err)
	w := &failingStackWriter{err: errors.New("redis down")}
	b := newStackBatcher(time.Hour, 10, zap.New(core), tb)
	b.writer = w
	stack := profileStack{
		TraceID: []byte{1}, SpanID: []byte{1}, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "a", FrameType: "jvm"}},
	}

	for i := 0; i < 3; i++ {
		b.pending = append(b.pending, stack)
		b.flush(context.Background())
	}
	assert.Equal(t, 1, logs.FilterMessage("autopilot profiles redis insert failed; dropping stacks").Len())
	metadatatest.AssertEqualOdigosAutopilotProfilesExporterCacheWriteFailedStacks(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 3}}, metricdatatest.IgnoreTimestamp())

	w.err = nil
	b.pending = append(b.pending, stack)
	b.flush(context.Background())
	assert.Equal(t, 1, logs.FilterMessage("autopilot profiles redis insert recovered").Len())
	metadatatest.AssertEqualOdigosAutopilotProfilesExporterCacheWrittenStacks(t, tt,
		[]metricdata.DataPoint[int64]{{Value: 1}}, metricdatatest.IgnoreTimestamp())

	duration, err := tt.GetMetric("otelcol_odigos.autopilot_profiles.exporter.cache_write.duration")
	require.NoError(t, err)
	hist, ok := duration.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "cache write duration must be a histogram")
	require.Len(t, hist.DataPoints, 1)
	assert.Equal(t, uint64(4), hist.DataPoints[0].Count, "every flush, failed or not, records its duration")
}
