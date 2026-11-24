package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const autopilotCallTreesMaxLimit = 1000

// listAutopilotCallTrees is the GET /api/v1/autopilot/call-trees handler.
func (a *API) listAutopilotCallTrees(c *gin.Context) {
	var params apitypes.ListAutopilotCallTreesParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := a.ListAutopilotCallTrees(c.Request.Context(), params)
	if err != nil {
		if errors.Is(err, errInvalidCallTreeParams) {
			writeError(c, http.StatusBadRequest, err)
			return
		}
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

var errInvalidCallTreeParams = errors.New("invalid call tree parameters")

// ListAutopilotCallTrees returns a workload container's transactions with their
// call trees.
func (a *API) ListAutopilotCallTrees(ctx context.Context, params apitypes.ListAutopilotCallTreesParams) (apitypes.AutopilotCallTreeList, error) {
	listParams := repo.CallTreeTransactionsParams{
		Namespace:      strings.TrimSpace(params.Namespace),
		WorkloadKind:   strings.TrimSpace(params.WorkloadKind),
		WorkloadName:   strings.TrimSpace(params.WorkloadName),
		ContainerName:  strings.TrimSpace(params.ContainerName),
		TransactionIDs: params.TransactionId,
		Search:         strings.TrimSpace(params.Search),
		Limit:          params.Limit,
		Offset:         params.Offset,
	}
	if listParams.Namespace == "" || listParams.WorkloadKind == "" || listParams.WorkloadName == "" || listParams.ContainerName == "" {
		return apitypes.AutopilotCallTreeList{}, errors.Join(errInvalidCallTreeParams, errors.New("namespace, workload_kind, workload_name and container_name are required"))
	}
	if listParams.Limit < 0 || listParams.Limit > autopilotCallTreesMaxLimit || listParams.Offset < 0 {
		return apitypes.AutopilotCallTreeList{}, errors.Join(errInvalidCallTreeParams, errors.New("limit must be 1-1000 and offset must not be negative"))
	}
	txs, err := a.Repo.ListCallTreeTransactions(ctx, listParams)
	if err != nil {
		return apitypes.AutopilotCallTreeList{}, err
	}
	return apimap.AutopilotCallTreeList(txs), nil
}
