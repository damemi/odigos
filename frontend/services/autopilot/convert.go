package autopilot

import (
	"github.com/odigos-io/odigos/common/autopilot"
	"github.com/odigos-io/odigos/frontend/graph/model"
)

// Only JVM frames carry Java package names the classifier understands.
const (
	frameTypeJVM     = "jvm"
	frameTypeHotspot = "hotspot"
)

// Libraries classify as application code. Odigos agent frames are dropped at
// ingest, and are shown as runtime if any remain.
var frameKindToModel = map[autopilot.FrameKind]model.AutopilotFrameKind{
	autopilot.FrameKindApp:             model.AutopilotFrameKindApp,
	autopilot.FrameKindRuntime:         model.AutopilotFrameKindRuntime,
	autopilot.FrameKindInstrumentation: model.AutopilotFrameKindRuntime,
}

// classifyFrame returns the frame's kind. Non-JVM frames are treated as application
// code, as they were before classification.
func classifyFrame(name, frameType string) model.AutopilotFrameKind {
	if frameType != frameTypeJVM && frameType != frameTypeHotspot {
		return model.AutopilotFrameKindApp
	}
	return frameKindToModel[autopilot.ClassifyFrame(name)]
}

// TransactionsToModel converts stored transactions to GraphQL models.
func TransactionsToModel(txs []Transaction) []*model.AutopilotTransaction {
	out := make([]*model.AutopilotTransaction, 0, len(txs))
	for _, tx := range txs {
		fns := make([]*model.AutopilotFunction, 0, len(tx.Functions))
		for _, fn := range tx.Functions {
			fns = append(fns, &model.AutopilotFunction{
				Name:       fn.Name,
				FrameType:  fn.FrameType,
				FrameKind:  classifyFrame(fn.Name, fn.FrameType),
				SampleType: fn.SampleType,
				SeenCount:  int(fn.SeenCount),
			})
		}
		out = append(out, &model.AutopilotTransaction{
			ID:        tx.ID,
			Operation: optionalString(tx.Operation),
			Kind:      optionalString(tx.Kind),
			SeenCount: int(tx.SeenCount),
			Functions: fns,
		})
	}
	return out
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// SourcesToModel converts per-container transaction counts to GraphQL models.
// Rows whose workload kind is not a known K8sResourceKind are dropped, since the
// enum could not be serialized.
func SourcesToModel(sources []SourceTransactions) []*model.AutopilotSource {
	out := make([]*model.AutopilotSource, 0, len(sources))
	for _, s := range sources {
		kind := model.K8sResourceKind(s.Kind)
		if !kind.IsValid() {
			continue
		}
		out = append(out, &model.AutopilotSource{
			ID:               &model.K8sWorkloadID{Namespace: s.Namespace, Kind: kind, Name: s.Name},
			ContainerName:    s.ContainerName,
			TransactionCount: int(s.TransactionCount),
		})
	}
	return out
}

// CallTrieToModel converts flat call-path trie nodes to GraphQL models.
func CallTrieToModel(nodes []CallTrieNode) []*model.AutopilotCallTrieNode {
	out := make([]*model.AutopilotCallTrieNode, 0, len(nodes))
	for _, n := range nodes {
		var parentID *string
		if n.ParentID != "" {
			parentID = &n.ParentID
		}
		out = append(out, &model.AutopilotCallTrieNode{
			ID:         n.ID,
			ParentID:   parentID,
			Name:       n.Name,
			FrameType:  n.FrameType,
			FrameKind:  classifyFrame(n.Name, n.FrameType),
			SampleType: n.SampleType,
			SeenCount:  int(n.SeenCount),
		})
	}
	return out
}
