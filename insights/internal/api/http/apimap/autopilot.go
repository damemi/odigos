package apimap

import (
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// callTreeRootParent is the parent stored for a call tree's roots.
const callTreeRootParent = "root"

// AutopilotCallTreeList maps call tree transactions to the wire type.
func AutopilotCallTreeList(in []repo.CallTreeTransaction) apitypes.AutopilotCallTreeList {
	items := make([]apitypes.AutopilotCallTreeTransaction, len(in))
	for i, tx := range in {
		nodes := make([]apitypes.AutopilotCallTreeNode, len(tx.Nodes))
		for j, n := range tx.Nodes {
			nodes[j] = FromCallTreeNode(n)
		}
		// Both stay empty, so absent on the wire, until the transaction is stored.
		name, dims := wireOperationIdentity(tx.Operation)
		items[i] = apitypes.AutopilotCallTreeTransaction{
			TransactionId:      tx.TransactionID,
			Operation:          tx.Operation,
			OperationName:      name,
			IdentityDimensions: dims,
			EntryKind:          tx.EntryKind,
			Nodes:              nodes,
		}
	}
	return apitypes.AutopilotCallTreeList{Count: len(items), Items: items}
}

// FromCallTreeNode maps one call tree node to the wire type. A transaction has
// a tree per sample type, and a call path has the same stored node id in each,
// so the wire id carries the sample type to stay unique within the transaction.
func FromCallTreeNode(n repo.CallTreeNode) apitypes.AutopilotCallTreeNode {
	out := apitypes.AutopilotCallTreeNode{
		Id:           callTreeNodeID(n.SampleType, n.NodeID),
		FunctionName: n.FunctionName,
		FrameType:    n.FrameType,
		SampleType:   n.SampleType,
	}
	if n.Parent != callTreeRootParent {
		out.ParentId = callTreeNodeID(n.SampleType, n.Parent)
	}
	return out
}

func callTreeNodeID(sampleType, nodeID string) string {
	return sampleType + ":" + nodeID
}
