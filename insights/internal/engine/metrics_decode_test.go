package engine

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func sumMetric(name string, dps ...*metricspb.NumberDataPoint) *metricspb.Metric {
	return &metricspb.Metric{
		Name: name,
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			IsMonotonic:            true,
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			DataPoints:             dps,
		}},
	}
}

func intDP(count int64, attrs ...*commonpb.KeyValue) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{
		Attributes:   attrs,
		TimeUnixNano: 1_000_000_000, // 1s since epoch, non-zero
		Value:        &metricspb.NumberDataPoint_AsInt{AsInt: count},
	}
}

// TestDecodeServiceGraphMetrics_RealEdge covers the raw-OTLP dotted attribute
// keys the connector emits (before prometheus normalisation), namespace
// extraction, request/failed pairing, and pod carry-through.
func TestDecodeServiceGraphMetrics_RealEdge(t *testing.T) {
	req := &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("k8s.pod.name", "gw-abc")}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: serviceGraphScopeName},
			Metrics: []*metricspb.Metric{
				sumMetric(serviceGraphRequestTotalName, intDP(10,
					strAttr("client", "frontend"),
					strAttr("server", "users"),
					strAttr("connection_type", ""),
					strAttr("client_service.name", "frontend"),
					strAttr("server_service.name", "users"),
					strAttr("client_k8s.namespace.name", "web"),
					strAttr("server_k8s.namespace.name", "api"),
				)),
				sumMetric(serviceGraphRequestFailedName, intDP(3,
					strAttr("client", "frontend"),
					strAttr("server", "users"),
					strAttr("connection_type", ""),
					strAttr("client_service.name", "frontend"),
					strAttr("server_service.name", "users"),
					strAttr("client_k8s.namespace.name", "web"),
					strAttr("server_k8s.namespace.name", "api"),
				)),
			},
		}},
	}}}

	edges := DecodeServiceGraphMetrics(req)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.ClientService != "frontend" || e.ServerService != "users" {
		t.Errorf("unexpected services: %q -> %q", e.ClientService, e.ServerService)
	}
	if e.ClientNamespace != "web" || e.ServerNamespace != "api" {
		t.Errorf("unexpected namespaces: %q -> %q", e.ClientNamespace, e.ServerNamespace)
	}
	if e.RequestCount != 10 || e.FailedCount != 3 {
		t.Errorf("unexpected counts: req=%d failed=%d", e.RequestCount, e.FailedCount)
	}
	if e.ClientIsVirtual || e.ServerIsVirtual {
		t.Errorf("both sides have service.name, should not be virtual (client=%v server=%v)", e.ClientIsVirtual, e.ServerIsVirtual)
	}
	if e.CollectorPod != "gw-abc" {
		t.Errorf("expected pod gw-abc, got %q", e.CollectorPod)
	}
}

// TestDecodeServiceGraphMetrics_VirtualNode covers a database/virtual edge:
// no server_service.name label ⇒ the server is virtual.
func TestDecodeServiceGraphMetrics_VirtualNode(t *testing.T) {
	req := &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: serviceGraphScopeName},
			Metrics: []*metricspb.Metric{
				sumMetric(serviceGraphRequestTotalName, intDP(5,
					strAttr("client", "users"),
					strAttr("server", "postgres"),
					strAttr("connection_type", "database"),
					strAttr("client_service.name", "users"),
					strAttr("client_k8s.namespace.name", "api"),
				)),
			},
		}},
	}}}

	edges := DecodeServiceGraphMetrics(req)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.ClientIsVirtual {
		t.Errorf("client has service.name, should not be virtual")
	}
	if !e.ServerIsVirtual {
		t.Errorf("server without service.name should be virtual")
	}
	if e.ConnectionType != "database" {
		t.Errorf("expected connection_type database, got %q", e.ConnectionType)
	}
	if e.RequestCount != 5 {
		t.Errorf("expected 5 requests, got %d", e.RequestCount)
	}
}

// TestDecodeServiceGraphMetrics_VirtualClient covers unmatched ingress: a
// SERVER span with no pairing CLIENT span, emitted as client="user" without
// client_service.name.
func TestDecodeServiceGraphMetrics_VirtualClient(t *testing.T) {
	req := &metricspb.MetricsData{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: serviceGraphScopeName},
			Metrics: []*metricspb.Metric{
				sumMetric(serviceGraphRequestTotalName, intDP(7,
					strAttr("client", "user"),
					strAttr("server", "db-permissions-leak"),
					strAttr("connection_type", "virtual_node"),
					strAttr("server_service.name", "db-permissions-leak"),
					strAttr("server_k8s.namespace.name", "nodejs-db-permissions-leak"),
				)),
			},
		}},
	}}}

	edges := DecodeServiceGraphMetrics(req)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if !e.ClientIsVirtual {
		t.Errorf("client without service.name should be virtual")
	}
	if e.ServerIsVirtual {
		t.Errorf("server has service.name, should not be virtual")
	}
	if e.ClientService != "user" || e.ServerService != "db-permissions-leak" {
		t.Errorf("unexpected services: %q -> %q", e.ClientService, e.ServerService)
	}
}
