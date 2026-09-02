package repo

import "time"

// ServiceGraphEdge is one directed client -> server edge as produced by the
// vendored servicegraph connector in the Odigos gateway and shipped to insights
// over OTLP. It is the write shape for UpsertServiceGraphEdges.
//
// RequestCount / FailedCount are the connector's cumulative counters at
// ObservedAt. Because trace load balancing can spread an edge's spans across
// several gateway pods, the counters are tracked per CollectorPod and the store
// keeps the latest value per (edge, pod); reads sum across pods. Exact totals
// are not the goal here — the blast-radius view needs edge existence, direction,
// virtual-ness, and rough magnitude/error presence.
type ServiceGraphEdge struct {
	ClientNamespace string
	ClientService   string
	ServerNamespace string
	ServerService   string
	ConnectionType  string
	// ClientIsVirtual is true when the client side is an uninstrumented caller
	// (the connector's synthetic "user" node for SERVER spans with no matching
	// CLIENT span) rather than a real instrumented service.
	ClientIsVirtual bool
	// ServerIsVirtual is true when the server side is an uninstrumented
	// dependency (DB, queue, external API) inferred by the connector's virtual
	// node logic rather than a real instrumented service.
	ServerIsVirtual bool
	// CollectorPod is the gateway pod that emitted the datapoint (resource
	// attribute k8s.pod.name), used to key per-pod cumulative counters. May be
	// empty when the pod name isn't stamped.
	CollectorPod string
	RequestCount uint64
	FailedCount  uint64
	ObservedAt   time.Time
}

// BlastRadiusNode is a single service (or virtual endpoint) in a blast-radius
// subgraph. Identity is (Namespace, Service); IsVirtual marks uninstrumented
// servers and the synthetic "user" caller so the UI can render them as leaves.
type BlastRadiusNode struct {
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	IsVirtual bool   `json:"isVirtual"`
}

// BlastRadiusEdge is a directed client -> server edge in a blast-radius
// subgraph, with the aggregated (summed across pods) request/failed counts and
// the most recent time it was seen.
type BlastRadiusEdge struct {
	ClientNamespace string    `json:"clientNamespace"`
	ClientService   string    `json:"clientService"`
	ServerNamespace string    `json:"serverNamespace"`
	ServerService   string    `json:"serverService"`
	ConnectionType  string    `json:"connectionType"`
	RequestCount    uint64    `json:"requestCount"`
	FailedCount     uint64    `json:"failedCount"`
	LastSeen        time.Time `json:"lastSeen"`
}

// BlastRadiusSubgraph is the neighborhood around an anchor service: the anchor
// itself plus every node/edge reachable within the requested hop depth, in both
// the inbound (callers) and outbound (callees) directions. It is the read shape
// returned by QueryBlastRadius and serialized by the HTTP API.
type BlastRadiusSubgraph struct {
	Root  BlastRadiusNode   `json:"root"`
	Nodes []BlastRadiusNode `json:"nodes"`
	Edges []BlastRadiusEdge `json:"edges"`
}
