package clickhouse

import (
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

// SourceKey identifies one instrumented source that has transactions. Attributes
// is the keyed resource-attribute map stored on insights.transactions.
// Namespace/Kind/Name/Container are extracted from that map when present so
// the current Kubernetes control loop can still talk to InstrumentationConfigs.
type SourceKey struct {
	Attributes    map[string]string
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
}

// SourceKeyFromAttributes builds a SourceKey from a resource-attribute map.
func SourceKeyFromAttributes(attrs map[string]string) SourceKey {
	return SourceKey{
		Attributes:    attrs,
		Namespace:     attrs[semconv.K8SNamespaceName],
		WorkloadKind:  attrs[consts.OdigosWorkloadKindAttribute],
		WorkloadName:  attrs[consts.OdigosWorkloadNameAttribute],
		ContainerName: attrs[semconv.K8SContainerName],
	}
}

// K8SComplete reports whether the source has a full Kubernetes workload identity.
func (s SourceKey) K8SComplete() bool {
	return s.Namespace != "" && s.WorkloadKind != "" && s.WorkloadName != "" && s.ContainerName != ""
}

// lookupAttributes returns the map used for insights.transactions lookups.
func (s SourceKey) lookupAttributes() map[string]string {
	if len(s.Attributes) > 0 {
		return s.Attributes
	}
	if !s.K8SComplete() {
		return nil
	}
	return map[string]string{
		semconv.K8SNamespaceName:           s.Namespace,
		consts.OdigosWorkloadKindAttribute: s.WorkloadKind,
		consts.OdigosWorkloadNameAttribute: s.WorkloadName,
		semconv.K8SContainerName:           s.ContainerName,
	}
}

// Transaction is one distinct transaction identity for a source.
type Transaction struct {
	ID           string
	RootSpanName string
	RootSpanKind string
}

// CallTrieNode is one call-path node for a transaction from
// tx_call_trie_structure. ParentID is empty for roots (ClickHouse Parent "root").
type CallTrieNode struct {
	ID         string
	ParentID   string
	Name       string
	FrameType  string
	SampleType string
}
