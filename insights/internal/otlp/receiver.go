// Package otlp hosts the OTLP gRPC receiver that ingests traces forwarded by
// the Odigos gateway collector. The receiver is a thin shell: it hands each
// request to the engine for decoding and scoring; all business logic lives
// there.
//
// It depends on go.opentelemetry.io/proto/otlp and google.golang.org/grpc
// directly rather than pulling in the full OpenTelemetry Collector dependency
// tree just to host a single TraceServiceServer.
package otlp

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"google.golang.org/grpc"
	// Blank import registers the gzip codec via init(). Real OTLP senders
	// default to gzip compression; without it the server rejects their
	// requests ("Decompressor is not installed for grpc-encoding gzip").
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/reflection"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
)

// Receiver implements coltracepb.TraceServiceServer. Incoming traces are
// handed to the engine (which talks to the Repository) for processing.
type Receiver struct {
	coltracepb.UnimplementedTraceServiceServer

	Engine *engine.Engine
	Logger *commonlogger.OdigosLogger

	receivedSpans  atomic.Uint64
	processedTrace atomic.Uint64
}

// Export decodes the request into per-trace structures and hands each one to
// the engine. The engine never returns errors (it logs and moves on), so a
// single bad trace cannot fail the whole batch.
func (s *Receiver) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	var n uint64
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			n += uint64(len(ss.GetSpans()))
		}
	}
	total := s.receivedSpans.Add(n)

	// Rewrap the request as the engine's TracesData view (same wire-shape,
	// different proto wrapper).
	traces := engine.DecodeRequest(&tracepb.TracesData{ResourceSpans: req.GetResourceSpans()})
	for _, t := range traces {
		s.Engine.Process(ctx, t)
		s.processedTrace.Add(1)
	}

	// Light, sampled visibility: roughly one line per 10k spans.
	if n > 0 && total%10000 < n && s.Logger != nil {
		s.Logger.Debug("otlp receiver progress",
			"spans_in_batch", n,
			"spans_total", total,
			"traces_processed", s.processedTrace.Load(),
		)
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// Server wraps a *grpc.Server preconfigured with the OTLP trace service.
type Server struct {
	addr     string
	logger   *commonlogger.OdigosLogger
	grpc     *grpc.Server
	listener net.Listener
}

// NewServer constructs (but does not start) a gRPC server bound to the OTLP
// trace and metrics services, backed by the given engine. Called by main at
// startup. Traces feed the anomaly-detection path; metrics carry the
// servicegraph connector's edges for the blast-radius topology.
func NewServer(addr string, eng *engine.Engine, logger *commonlogger.OdigosLogger) *Server {
	gs := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(gs, &Receiver{Engine: eng, Logger: logger})
	colmetricspb.RegisterMetricsServiceServer(gs, &MetricsReceiver{Engine: eng, Logger: logger})

	// Register gRPC reflection so grpcurl works against the in-cluster service.
	// The endpoint is internal-only and costs a few KB at startup.
	reflection.Register(gs)

	return &Server{
		addr:   addr,
		logger: logger,
		grpc:   gs,
	}
}

// Start binds the listener and serves gRPC in a goroutine. It blocks only on
// bind; the serve loop runs asynchronously and reports fatal errors through
// the returned channel.
func (s *Server) Start() (<-chan error, error) {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", s.addr, err)
	}
	s.listener = lis

	errCh := make(chan error, 1)
	go func() {
		if s.logger != nil {
			s.logger.Info("otlp gRPC server listening", "addr", s.addr)
		}
		if err := s.grpc.Serve(lis); err != nil {
			errCh <- err
		}
		close(errCh)
	}()
	return errCh, nil
}

// Stop gracefully shuts the gRPC server down.
func (s *Server) Stop() {
	if s.grpc != nil {
		s.grpc.GracefulStop()
	}
}
