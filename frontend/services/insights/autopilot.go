package insights

import (
	"context"
	"net/http"
	"strconv"
)

// ListAutopilotCallTreesParams selects one workload container's call trees.
type ListAutopilotCallTreesParams struct {
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	// TransactionIDs keeps only these transactions when non-empty.
	TransactionIDs []int64
	// Search keeps the transactions whose id contains it.
	Search string
	// Limit caps the transactions returned; 0 means all.
	Limit int
	// Offset skips this many matching transactions.
	Offset int
}

// AutopilotCallTreeTransaction is one transaction with its call trees, one per
// sample type, as a flat node list.
type AutopilotCallTreeTransaction struct {
	TransactionID int64 `json:"transaction_id"`
	// Operation, OperationName, IdentityDimensions and EntryKind are empty until
	// insights stores the transaction. OperationName and IdentityDimensions are
	// Operation split into its name and identity dimensions.
	Operation          string                     `json:"operation,omitempty"`
	OperationName      string                     `json:"operation_name,omitempty"`
	IdentityDimensions []TransactionIdentityValue `json:"identity_dimensions,omitempty"`
	EntryKind          string                     `json:"entry_kind,omitempty"`
	Nodes              []AutopilotCallTreeNode    `json:"nodes"`
}

// AutopilotCallTreeNode is one function on a call path. ID is unique within the
// transaction; ParentID is empty for roots.
type AutopilotCallTreeNode struct {
	ID           string `json:"id"`
	ParentID     string `json:"parent_id,omitempty"`
	FunctionName string `json:"function_name"`
	FrameType    string `json:"frame_type"`
	SampleType   string `json:"sample_type"`
}

// ListAutopilotCallTrees returns a workload container's transactions with their
// call trees (GET /api/v1/autopilot/call-trees).
func (c *Client) ListAutopilotCallTrees(ctx context.Context, params ListAutopilotCallTreesParams) ([]AutopilotCallTreeTransaction, error) {
	endpoint := c.apiEndpoint("autopilot", "call-trees")
	query := endpoint.Query()
	query.Set("namespace", params.Namespace)
	query.Set("workload_kind", params.WorkloadKind)
	query.Set("workload_name", params.WorkloadName)
	query.Set("container_name", params.ContainerName)
	for _, id := range params.TransactionIDs {
		query.Add("transaction_id", strconv.FormatInt(id, 10))
	}
	if params.Search != "" {
		query.Set("search", params.Search)
	}
	if params.Limit > 0 {
		query.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.Offset > 0 {
		query.Set("offset", strconv.Itoa(params.Offset))
	}
	endpoint.RawQuery = query.Encode()
	return doList[AutopilotCallTreeTransaction](ctx, c, http.MethodGet, endpoint, nil)
}
