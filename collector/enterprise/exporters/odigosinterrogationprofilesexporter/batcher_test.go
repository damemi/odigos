package odigosinterrogationprofilesexporter

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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
	b := newStackBatcher(time.Hour, 2, zap.NewNop())
	b.start(w)
	t.Cleanup(func() { _ = b.stop() })

	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	b.recordStack(profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "a", FrameType: "jvm"}},
	})
	assert.Equal(t, int32(0), w.calls.Load())

	b.recordStack(profileStack{
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
	b := newStackBatcher(time.Hour, 1, zap.NewNop())
	b.start(w)
	t.Cleanup(func() {
		close(w.block)
		_ = b.stop()
	})

	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	b.recordStack(profileStack{
		TraceID: traceID, SpanID: spanID, SampleType: "samples",
		Frames: []profileFrame{{FunctionName: "a", FrameType: "jvm"}},
	})
	<-w.started

	for i := 0; i < defaultFlushThreshold+8; i++ {
		b.recordStack(profileStack{
			TraceID: traceID, SpanID: spanID, SampleType: "samples",
			Frames: []profileFrame{{FunctionName: "x", FrameType: "jvm"}},
		})
	}
	assert.Equal(t, int32(1), w.calls.Load(), "batcher must not start another redis write while the first is in flight")
}
