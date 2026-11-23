package store

// SourceKey identifies one workload container that has call trees. Keys for a
// whole workload (recommendations, rules) leave ContainerName empty.
type SourceKey struct {
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
}

// K8SComplete reports whether the source has a full Kubernetes workload identity.
func (s SourceKey) K8SComplete() bool {
	return s.Namespace != "" && s.WorkloadKind != "" && s.WorkloadName != "" && s.ContainerName != ""
}

// Transaction is one distinct transaction identity for a source.
type Transaction struct {
	ID           string
	RootSpanName string
	RootSpanKind string
}

// CallTreeNode is one call-path node of a transaction. ParentID is empty for
// roots.
type CallTreeNode struct {
	ID         string
	ParentID   string
	Name       string
	FrameType  string
	SampleType string
}
