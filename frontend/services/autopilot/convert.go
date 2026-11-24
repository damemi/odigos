package autopilot

import (
	"strconv"
	"strings"

	"github.com/odigos-io/odigos/common/autopilot"
	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/services/insights"
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

// TransactionIDs parses GraphQL transaction ids for the insights API. Ids that
// aren't insights ids (signed 64-bit integers) match nothing, so they are
// dropped; ok is false when none is left.
func TransactionIDs(ids []string) (parsed []int64, ok bool) {
	for _, id := range ids {
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil {
			parsed = append(parsed, n)
		}
	}
	return parsed, len(parsed) > 0
}

// TransactionsToModel converts insights' call tree transactions to GraphQL
// models, keeping their order.
func TransactionsToModel(txs []insights.AutopilotCallTreeTransaction) []*model.AutopilotTransaction {
	out := make([]*model.AutopilotTransaction, 0, len(txs))
	for _, tx := range txs {
		out = append(out, &model.AutopilotTransaction{
			ID:                 strconv.FormatInt(tx.TransactionID, 10),
			OperationName:      optionalString(tx.OperationName),
			IdentityDimensions: insights.TransactionIdentityValuesToModel(tx.IdentityDimensions),
			Kind:               optionalString(tx.EntryKind),
			CallTree:           callTreeToModel(tx.Nodes),
		})
	}
	return out
}

func callTreeToModel(nodes []insights.AutopilotCallTreeNode) []*model.AutopilotCallTreeNode {
	out := make([]*model.AutopilotCallTreeNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, &model.AutopilotCallTreeNode{
			ID:         n.ID,
			ParentID:   optionalString(n.ParentID),
			Name:       n.FunctionName,
			FrameType:  n.FrameType,
			FrameKind:  classifyFrame(n.FunctionName, n.FrameType),
			SampleType: n.SampleType,
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
