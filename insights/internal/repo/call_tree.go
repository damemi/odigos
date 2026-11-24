package repo

// CallTreeNode is one node of an autopilot call tree: a function on the call
// paths of a transaction's profile stacks. Parent is "root" for roots,
// otherwise the parent's NodeID.
type CallTreeNode struct {
	SourceID      int64
	TransactionID int64
	// SampleType is "events" or "samples".
	SampleType   string
	NodeID       string
	Parent       string
	FunctionName string
	// FrameType is "jvm" or "hotspot".
	FrameType string
}

// CallTreeTransactionsParams selects the call trees of one workload container.
type CallTreeTransactionsParams struct {
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	// TransactionIDs, when non-nil, keeps only these transactions. An empty
	// non-nil slice matches nothing.
	TransactionIDs []int64
	// Search keeps the transactions whose id contains it.
	Search string
	// Limit caps the transactions returned; 0 means all.
	Limit int
	// Offset skips this many matching transactions.
	Offset int
}

// CallTreeTransaction is one transaction's call tree nodes, of every sample
// type, with the transaction's operation and entry kind when insights has
// stored it.
type CallTreeTransaction struct {
	TransactionID int64
	// Operation and EntryKind are empty until the transaction is stored.
	Operation string
	EntryKind string
	Nodes     []CallTreeNode
}
