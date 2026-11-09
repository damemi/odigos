package engine

import (
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Scope and metric names emitted by the vendored servicegraph connector. This
// side-channel receives the RAW OTLP metrics (the gateway's prometheus exporter
// is a separate pipeline), so the names are the connector's own - without the
// "servicegraph_" namespace prefix prometheus would add.
const (
	serviceGraphScopeName         = "traces_service_graph"
	serviceGraphRequestTotalName  = "traces_service_graph_request_total"
	serviceGraphRequestFailedName = "traces_service_graph_request_failed_total"
)

// Datapoint attribute keys set by the connector's buildDimensions. The base
// client/server names and connection_type are always present; the *_service.name
// and *_k8s.namespace.name labels come from the configured dimensions
// (service.name is always added; k8s.namespace.name is forced on when insights
// is active). On the raw OTLP path the keys carry dots - prometheus would
// normalise them to underscores, but we read before that - so we accept both to
// stay robust.
var (
	sgAttrClientNamespace = []string{"client_" + semconv.K8SNamespaceName, "client_k8s_namespace_name"}
	sgAttrServerNamespace = []string{"server_" + semconv.K8SNamespaceName, "server_k8s_namespace_name"}
	sgAttrClientService   = []string{"client_" + semconv.ServiceName, "client_service_name"}
	sgAttrServerService   = []string{"server_" + semconv.ServiceName, "server_service_name"}
)

const (
	sgAttrClient         = "client"
	sgAttrServer         = "server"
	sgAttrConnectionType = "connection_type"
)

// attrAny returns the first present value among keys, and whether any matched.
func attrAny(attrs map[string]string, keys []string) (string, bool) {
	for _, k := range keys {
		if v, ok := attrs[k]; ok {
			return v, true
		}
	}
	return "", false
}

// sgEdgeKey uniquely identifies an edge datapoint within one decode so
// request_total and request_failed_total for the same edge merge into one row.
type sgEdgeKey struct {
	clientNs, client, serverNs, server, connType, pod string
}

// DecodeServiceGraphMetrics flattens an OTLP MetricsData from the servicegraph
// connector into per-edge rows. It reads the request_total and (when present)
// request_failed_total sum datapoints, pairing an edge's counts into a single
// ServiceGraphEdge. The gateway pod name (resource attribute k8s.pod.name,
// stamped by the metrics pipeline) is carried onto every edge so the store can
// keep per-pod cumulative values. Datapoints missing a client or server name
// are dropped.
func DecodeServiceGraphMetrics(req *metricspb.MetricsData) []repo.ServiceGraphEdge {
	if req == nil {
		return nil
	}
	edges := map[sgEdgeKey]*repo.ServiceGraphEdge{}

	for _, rm := range req.GetResourceMetrics() {
		resourceAttrs := flattenAttrs(rm.GetResource().GetAttributes())
		pod := resourceAttrs[semconv.K8SPodName]

		for _, sm := range rm.GetScopeMetrics() {
			// Only the servicegraph connector's scope. Tolerate an empty scope
			// name (some pipelines drop it) rather than silently discarding.
			if name := sm.GetScope().GetName(); name != "" && name != serviceGraphScopeName {
				continue
			}
			for _, m := range sm.GetMetrics() {
				var failed bool
				switch m.GetName() {
				case serviceGraphRequestTotalName:
					failed = false
				case serviceGraphRequestFailedName:
					failed = true
				default:
					continue
				}
				sum := m.GetSum()
				if sum == nil {
					continue
				}
				for _, dp := range sum.GetDataPoints() {
					attrs := flattenAttrs(dp.GetAttributes())
					client := attrs[sgAttrClient]
					server := attrs[sgAttrServer]
					if client == "" || server == "" {
						continue
					}
					clientNs, _ := attrAny(attrs, sgAttrClientNamespace)
					serverNs, _ := attrAny(attrs, sgAttrServerNamespace)
					key := sgEdgeKey{
						clientNs: clientNs,
						client:   client,
						serverNs: serverNs,
						server:   server,
						connType: attrs[sgAttrConnectionType],
						pod:      pod,
					}
					ts := time.Unix(0, int64(dp.GetTimeUnixNano()))
					e := edges[key]
					if e == nil {
						// A virtual (uninstrumented) node has no matching span
						// on that side, so the connector never emits its
						// service.name label. Missing client_service.name is
						// the synthetic "user" caller; missing
						// server_service.name is a virtual dependency.
						_, clientInstrumented := attrAny(attrs, sgAttrClientService)
						_, serverInstrumented := attrAny(attrs, sgAttrServerService)
						e = &repo.ServiceGraphEdge{
							ClientNamespace: clientNs,
							ClientService:   client,
							ServerNamespace: serverNs,
							ServerService:   server,
							ConnectionType:  attrs[sgAttrConnectionType],
							ClientIsVirtual: !clientInstrumented,
							ServerIsVirtual: !serverInstrumented,
							CollectorPod:    pod,
							ObservedAt:      ts,
						}
						edges[key] = e
					}
					count := datapointCount(dp)
					if failed {
						e.FailedCount += count
					} else {
						e.RequestCount += count
					}
					if ts.After(e.ObservedAt) {
						e.ObservedAt = ts
					}
				}
			}
		}
	}

	out := make([]repo.ServiceGraphEdge, 0, len(edges))
	for _, e := range edges {
		if e.ObservedAt.IsZero() {
			e.ObservedAt = time.Now()
		}
		out = append(out, *e)
	}
	return out
}

// datapointCount reads a NumberDataPoint's value as a non-negative count,
// handling both the int and double OTLP encodings.
func datapointCount(dp *metricspb.NumberDataPoint) uint64 {
	if v := dp.GetAsInt(); v > 0 {
		return uint64(v)
	}
	if d := dp.GetAsDouble(); d > 0 {
		return uint64(d)
	}
	return 0
}
