package services

import (
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/odigos-io/odigos/frontend/graph/model"
	collectormetrics "github.com/odigos-io/odigos/frontend/services/collector_metrics"
)

type ServiceGraphEdges = map[string]map[string]collectormetrics.ServiceGraphEdge

// ServiceGraphNodeAttributesForServer prepares labels for the TO node row (metrics are edge-centric; API is node-centric).
func ServiceGraphNodeAttributesForServer(attrs map[string]string) map[string]string {
	// peer_* labels also describe the target, on virtual-node and database edges.
	out := map[string]string{}
	maps.Copy(out, serviceGraphLabelsForPrefix(attrs, "peer"))
	maps.Copy(out, serviceGraphLabelsForPrefix(attrs, "server"))
	return out
}

// ServiceGraphNodeAttributesForClient prepares labels for the FROM node row (inbound peers).
func ServiceGraphNodeAttributesForClient(attrs map[string]string) map[string]string {
	return serviceGraphLabelsForPrefix(attrs, "client")
}

// serviceGraphLabelsForPrefix filters to one edge side, strips the role prefix, converts underscores to dots for UI,
// and omits *service_name (already in serviceName).
func serviceGraphLabelsForPrefix(attrs map[string]string, prefix string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	prefixUnderscore := prefix + "_"
	// Prometheus normalizes service.name → service_name on the wire.
	skipServiceName := prefixUnderscore + "service_name"
	out := make(map[string]string)
	for k, v := range attrs {
		if k == prefix || !strings.HasPrefix(k, prefixUnderscore) {
			continue
		}
		if k == skipServiceName {
			continue
		}
		suffix := strings.TrimPrefix(k, prefixUnderscore)
		if suffix == "" {
			continue
		}
		// turns a metric label tail (underscores) into a UI-friendly dotted key.
		out[strings.ReplaceAll(suffix, "_", ".")] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BaseServiceName extracts the service name from a composite node ID.
// Composite IDs follow the format "serviceName|dim1|dim2|..." where the
// service name is everything before the first "|" separator.
// If the ID contains no "|", it is returned as-is.
func BaseServiceName(compositeID string) string {
	if i := strings.Index(compositeID, "|"); i >= 0 {
		return compositeID[:i]
	}
	return compositeID
}

// Builds []*NonIdentifyingAttribute from a string map (e.g. service-graph labels).
func convertStringMapToNonIdentifyingAttributes(m map[string]string) []*model.NonIdentifyingAttribute {
	if len(m) == 0 {
		return []*model.NonIdentifyingAttribute{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*model.NonIdentifyingAttribute, 0, len(keys))
	for _, k := range keys {
		out = append(out, &model.NonIdentifyingAttribute{Key: k, Value: m[k]})
	}
	return out
}

// Maps a collector service-graph edge metric to the GraphQL ServiceMapToSource type.
func EdgeToModel(compositeKey string, edge collectormetrics.ServiceGraphEdge, nodeAttrs map[string]string) *model.ServiceMapToSource {
	return &model.ServiceMapToSource{
		NodeID:         compositeKey,
		ServiceName:    BaseServiceName(compositeKey),
		IsVirtual:      edge.ToNodeIsVirtual,
		Requests:       int(edge.RequestCount),
		DateTime:       edge.LastUpdated.Format(time.RFC3339),
		NodeAttributes: convertStringMapToNonIdentifyingAttributes(nodeAttrs),
	}
}

// mergeEdge aggregates an edge into m keyed by base service name and namespace,
// summing request counts and keeping the most recent timestamp.
func mergeEdge(m map[string]*model.ServiceMapToSource, compositeKey, namespace string, edge collectormetrics.ServiceGraphEdge, nodeAttrs map[string]string) {
	key := BaseServiceName(compositeKey) + "|" + namespace
	if existing, ok := m[key]; ok {
		existing.Requests += int(edge.RequestCount)
		ts := edge.LastUpdated.Format(time.RFC3339)
		if ts > existing.DateTime {
			existing.DateTime = ts
			existing.NodeAttributes = convertStringMapToNonIdentifyingAttributes(nodeAttrs)
		}
		if edge.ToNodeIsVirtual {
			existing.IsVirtual = true
		}
	} else {
		m[key] = EdgeToModel(compositeKey, edge, nodeAttrs)
	}
}

func mapValues(m map[string]*model.ServiceMapToSource) []*model.ServiceMapToSource {
	out := make([]*model.ServiceMapToSource, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// PeerSources finds all inbound and outbound peers for a given base service name.
// The allEdges map may contain composite keys (e.g. "redis|ns1") so we compare
// against the base name extracted from each key. When namespace is set, only the
// service in that namespace matches. Results are deduplicated by base service name
// and namespace, summing request counts and keeping the latest timestamp.
func PeerSources(allEdges ServiceGraphEdges, serviceName, namespace string) *model.PeerSources {
	inboundMap := make(map[string]*model.ServiceMapToSource)
	outboundMap := make(map[string]*model.ServiceMapToSource)
	isQueried := func(nodeKey, nodeNamespace string) bool {
		return BaseServiceName(nodeKey) == serviceName && (namespace == "" || nodeNamespace == "" || nodeNamespace == namespace)
	}

	for callerKey, targets := range allEdges {
		for targetKey, edge := range targets {
			callerNamespace, targetNamespace := edge.Attributes["client_k8s_namespace_name"], edge.Attributes["server_k8s_namespace_name"]
			if isQueried(callerKey, callerNamespace) {
				mergeEdge(outboundMap, targetKey, targetNamespace, edge, ServiceGraphNodeAttributesForServer(edge.Attributes))
			}
			if isQueried(targetKey, targetNamespace) {
				// ToNodeIsVirtual describes the target; a caller is never virtual.
				caller := edge
				caller.ToNodeIsVirtual = false
				mergeEdge(inboundMap, callerKey, callerNamespace, caller, ServiceGraphNodeAttributesForClient(edge.Attributes))
			}
		}
	}

	return &model.PeerSources{
		Inbound:  mapValues(inboundMap),
		Outbound: mapValues(outboundMap),
	}
}
