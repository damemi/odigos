package opamp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-telemetry/opamp-go/protobufs"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos/connectors/base/domain/collectormetrics"
)

func TestOwnMetricsOfferWaitsForVerifiedDescription(t *testing.T) {
	s := New(self(), newFakeCollectors(), newFakeDesired())
	s.Metrics = collectormetrics.NewCache()
	s.PublicEndpoint = "wss://central.example/v1/opamp"
	h := headersFor(identity(nil))
	h.Set(collectorgateway.HeaderMetricsAuthorization, "Bearer scoped-metrics-token")
	answers := exchange(t, s, h,
		&protobufs.AgentToServer{Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics)},
		&protobufs.AgentToServer{AgentDescription: description(supervisorDescription())},
		&protobufs.AgentToServer{},
	)
	if answers[0].GetConnectionSettings() != nil {
		t.Fatal("offered metrics before verifying the Collector")
	}
	offer := answers[1].GetConnectionSettings().GetOwnMetrics()
	if offer == nil || offer.GetDestinationEndpoint() != "https://central.example/v1/metrics" {
		t.Fatalf("own metrics offer = %+v", offer)
	}
	if got := offer.GetHeaders().GetHeaders(); len(got) != 1 || got[0].GetKey() != "Authorization" || got[0].GetValue() != "Bearer scoped-metrics-token" {
		t.Fatalf("metrics authorization headers = %+v", got)
	}
	if answers[2].GetConnectionSettings() != nil {
		t.Fatal("repeated an unchanged own-metrics offer")
	}
}

func TestMetricsReceiverUsesAuthenticatedCollectorIdentity(t *testing.T) {
	cache := collectormetrics.NewCache()
	collectors := newFakeCollectors()
	s := New(self(), collectors, newFakeDesired())
	s.Metrics = cache
	metric := &metricspb.Metric{Name: "otelcol_receiver_accepted_spans", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
		DataPoints: []*metricspb.NumberDataPoint{{Value: &metricspb.NumberDataPoint_AsInt{AsInt: 17}}},
	}}}
	rm := &metricspb.ResourceMetrics{
		Resource:     &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{Key: "service.instance.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "instance-1"}}}}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{metric}}},
	}
	encoded, err := proto.Marshal(rm)
	if err != nil {
		t.Fatal(err)
	}
	body := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), encoded)
	request := httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(body))
	request.Header = headersFor(identity(nil))
	request.Header.Set("Content-Type", "application/x-protobuf")
	response := httptest.NewRecorder()
	s.metricsHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics POST = %d", response.Code)
	}
	snapshot, ok := cache.Snapshot(testRegion)
	if !ok || snapshot.AcceptedSpans == nil || *snapshot.AcceptedSpans != 17 {
		t.Fatalf("snapshot = %+v, present %t", snapshot, ok)
	}
	if len(collectors.metrics) != 1 || collectors.metrics[0].Snapshot.AcceptedSpans == nil || *collectors.metrics[0].Snapshot.AcceptedSpans != 17 {
		t.Fatalf("stored metrics = %+v", collectors.metrics)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/x-protobuf")
	response = httptest.NewRecorder()
	s.metricsHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics POST = %d", response.Code)
	}
}
