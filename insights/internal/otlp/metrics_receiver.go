package otlp

import (
	"context"
	"sync/atomic"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/engine"
)

// MetricsReceiver implements colmetricspb.MetricsServiceServer. It receives the
// servicegraph connector's metrics (fanned out by the gateway on a dedicated
// OTLP pipeline) and hands them to the engine, which decodes them into edges and
// persists them for the blast-radius topology. Registered on the same gRPC
// server as the trace Receiver.
type MetricsReceiver struct {
	colmetricspb.UnimplementedMetricsServiceServer

	Engine *engine.Engine
	Logger *commonlogger.OdigosLogger

	receivedEdges atomic.Uint64
}

// Export decodes the request into servicegraph edges and hands them to the
// engine. Like the trace path the engine never returns an error, so one bad
// export can't fail the whole batch.
func (s *MetricsReceiver) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	edges := engine.DecodeServiceGraphMetrics(&metricspb.MetricsData{ResourceMetrics: req.GetResourceMetrics()})
	if len(edges) > 0 {
		s.Engine.ProcessServiceGraphMetrics(ctx, edges)
		total := s.receivedEdges.Add(uint64(len(edges)))
		if s.Logger != nil && total%1000 < uint64(len(edges)) {
			s.Logger.Debug("otlp metrics receiver progress",
				"edges_in_batch", len(edges),
				"edges_total", total,
			)
		}
	}
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}
