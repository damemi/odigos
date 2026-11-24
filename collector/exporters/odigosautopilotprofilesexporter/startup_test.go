package odigosautopilotprofilesexporter

import (
	"context"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.uber.org/zap"

	"github.com/odigos-io/odigos/common/consts"
)

// The gateway may start before cacheDb Redis is up. The exporter must not be
// permanently disabled: once Redis listens on the endpoint, stacks are written.
func TestExporter_WritesOnceRedisStartsLater(t *testing.T) {
	// Reserve a free port, then release it so nothing listens yet.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	e := newExporter(zap.NewNop(), componenttest.NewNopTelemetrySettings(), &Config{
		RedisEndpoint:            addr,
		GroupByTraceWaitDuration: defaultGroupByTraceWaitDuration,
		FlushInterval:            10 * time.Millisecond,
		FlushThreshold:           defaultFlushThreshold,
	})
	require.NoError(t, e.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	require.True(t, e.batcher.enabled(), "exporter must start even when redis is unreachable")

	mr := miniredis.NewMiniRedis()
	require.NoError(t, mr.StartAddr(addr))
	t.Cleanup(mr.Close)

	traceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	key := consts.RedisAutopilotProfileKeyPrefix + hex.EncodeToString(traceID)
	require.Eventually(t, func() bool {
		e.batcher.recordStack(context.Background(), profileStack{
			TraceID: traceID, SpanID: []byte{1, 2, 3, 4, 5, 6, 7, 8}, SampleType: "samples",
			Frames: []profileFrame{{FunctionName: "a", FrameType: "jvm"}},
		})
		mr.Select(consts.RedisDBAutopilotProfilesTransactionsCorrelation)
		return mr.Exists(key)
	}, 5*time.Second, 20*time.Millisecond, "stacks must reach redis once it starts")
}
