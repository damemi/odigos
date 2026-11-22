package repo

import (
	"hash/fnv"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

// SourcePlatform names where a source runs. Each platform with typed identity
// fields has its own table (k8s_source_identity, ...).
type SourcePlatform string

const (
	SourcePlatformK8s SourcePlatform = "k8s"
	// SourcePlatformUnknown is an identity no platform recognizes. It is stored
	// with its raw attributes only.
	SourcePlatformUnknown SourcePlatform = "unknown"
)

// Source is the Odigos source a transaction belongs to, e.g. a k8s workload
// container. Many transactions share one source.
type Source struct {
	ID       int64
	Platform SourcePlatform
	// K8s is set when Platform is SourcePlatformK8s.
	K8s *K8sSource
}

type K8sSource struct {
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
}

// UnknownNamespace is the namespace of a source whose platform has none.
// The Postgres queries yield the same value (see transactionNamespace).
const UnknownNamespace = "unknown"

// SourceNamespace returns the namespace a source's services are grouped
// under: the k8s namespace for a k8s source, UnknownNamespace otherwise.
func SourceNamespace(identity map[string]string) string {
	if k8s, ok := k8sSourceFromIdentity(identity); ok {
		return k8s.Namespace
	}
	return UnknownNamespace
}

// HashSourceID derives a stable, non-negative id from a source identity, so
// every replica maps the same source to the same id with no DB round-trip.
func HashSourceID(identity map[string]string) int64 {
	h := fnv.New64a()
	writeSourceIdentity(h, identity)
	return int64(h.Sum64() >> 1)
}

// NewSource builds the Source for a source identity, detecting its platform
// from the attributes it holds.
func NewSource(identity map[string]string) Source {
	s := Source{ID: HashSourceID(identity), Platform: SourcePlatformUnknown}
	if k8s, ok := k8sSourceFromIdentity(identity); ok {
		s.Platform = SourcePlatformK8s
		s.K8s = &k8s
	}
	return s
}

func k8sSourceFromIdentity(identity map[string]string) (K8sSource, bool) {
	kind, name := sourceidentity.K8sWorkloadKindAndNameFromResourceAttributesMap(identity)
	s := K8sSource{
		Namespace:     identity[semconv.K8SNamespaceName],
		WorkloadKind:  kind,
		WorkloadName:  name,
		ContainerName: identity[semconv.K8SContainerName],
	}
	return s, s.Namespace != "" && s.WorkloadKind != "" && s.WorkloadName != "" && s.ContainerName != ""
}
