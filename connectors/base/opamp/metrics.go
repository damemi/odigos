package opamp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

const maxMetricsBody = 4 << 20

// metricsHandler receives the OTLP/HTTP own telemetry enabled by an OpAMP connection offer.
// The gateway authenticates every request and forwards only its bound identity; this second
// check prevents a wrong route from writing another connector's regional snapshot.
func (s *Server) metricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-protobuf") {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		id, refusal := Admit(r.Header, s.Self)
		if refusal != RefusalNone {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := store.CollectorKey{Region: id.Binding.Region}
		rec, err := s.Collectors.GetManagedCollector(r.Context(), key)
		if err != nil {
			if errors.Is(err, store.ErrCollectorNotFound) {
				w.WriteHeader(http.StatusUnauthorized)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		if VerifyCollector(id, &Collector{NetworkID: rec.NetworkID, CollectorID: rec.CollectorID}) != RefusalNone {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMetricsBody))
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		resources, err := decodeMetricsRequest(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		instanceID := metricsInstanceID(resources)
		if instanceID == "" {
			instanceID = id.Binding.CollectorID
		}
		if s.Metrics == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.metricsMu.Lock()
		defer s.metricsMu.Unlock()
		s.Metrics.Record(key.Region, instanceID, resources, time.Now().UTC())
		snapshot, found := s.Metrics.Snapshot(key.Region)
		if !found {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := s.Collectors.RecordCollectorMetrics(r.Context(), key, snapshot.Stored(), snapshot.ObservedAt); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	})
}

// OTLP ExportMetricsServiceRequest is a repeated ResourceMetrics field at tag 1. Decode
// that small envelope directly to avoid importing the generated gRPC service package into
// an HTTP-only receiver; ResourceMetrics itself still uses the official OTLP protobuf.
func decodeMetricsRequest(body []byte) ([]*metricspb.ResourceMetrics, error) {
	var resources []*metricspb.ResourceMetrics
	for len(body) > 0 {
		number, kind, n := protowire.ConsumeTag(body)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		body = body[n:]
		if number == 1 && kind == protowire.BytesType {
			value, n := protowire.ConsumeBytes(body)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			var resource metricspb.ResourceMetrics
			if err := proto.Unmarshal(value, &resource); err != nil {
				return nil, err
			}
			resources = append(resources, &resource)
			body = body[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(number, kind, body)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		body = body[n:]
	}
	return resources, nil
}

func metricsInstanceID(resources []*metricspb.ResourceMetrics) string {
	for _, rm := range resources {
		for _, kv := range rm.GetResource().GetAttributes() {
			if kv.GetKey() == "service.instance.id" && kv.GetValue().GetStringValue() != "" {
				return kv.GetValue().GetStringValue()
			}
		}
	}
	return ""
}
