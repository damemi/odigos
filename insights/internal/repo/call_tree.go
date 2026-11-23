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
