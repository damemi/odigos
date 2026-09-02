package http

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

const findingsListLimit = 1000

// API holds the shared dependencies for the HTTP handlers. Reads go through the
// Repository alone; write endpoints additionally need the engine-backed Admin
// (wired via SetAdmin once the engine exists), because a DB write must also
// invalidate/reload the engine's in-memory snapshots to take effect live. The
// handlers, grouped by resource across sibling files, map repo values to apimap
// wire types.
type API struct {
	Repo  repo.Repository
	admin atomic.Pointer[Admin]
}

// parseTransactionID reads `:transaction_id` from the URL. On a parse error it
// writes a 400 and returns ok=false.
func parseTransactionID(c *gin.Context) (int64, bool) {
	raw := c.Param("transaction_id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return 0, false
	}
	return id, true
}

// bindListFindingsParams reads OpenAPI ListFindingsParams from the query string.
func bindListFindingsParams(c *gin.Context) (apitypes.ListFindingsParams, error) {
	var params apitypes.ListFindingsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		return apitypes.ListFindingsParams{}, err
	}
	return params, nil
}

// bindListAnomaliesParams reads OpenAPI ListAnomaliesParams from the query string.
func bindListAnomaliesParams(c *gin.Context) (apitypes.ListAnomaliesParams, error) {
	var params apitypes.ListAnomaliesParams
	if err := c.ShouldBindQuery(&params); err != nil {
		return apitypes.ListAnomaliesParams{}, err
	}
	return params, nil
}

// bindListGuardrailViolationsParams reads OpenAPI ListGuardrailViolationsParams
// from the query string.
func bindListGuardrailViolationsParams(c *gin.Context) (apitypes.ListGuardrailViolationsParams, error) {
	var params apitypes.ListGuardrailViolationsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		return apitypes.ListGuardrailViolationsParams{}, err
	}
	return params, nil
}

// bindListTransactionsParams reads OpenAPI ListTransactionsParams from the query.
func bindListTransactionsParams(c *gin.Context) (apitypes.ListTransactionsParams, error) {
	var params apitypes.ListTransactionsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		return apitypes.ListTransactionsParams{}, err
	}
	return params, nil
}

func findingsTimeRangeFromParams(windowHours apitypes.WindowHours, from apitypes.FromTime, to apitypes.ToTime) (repo.FindingsTimeRange, error) {
	return repo.ResolveFindingsTimeRange(time.Now(), int(windowHours), string(from), string(to))
}

func findingListFilterFromTimeParams(
	windowHours apitypes.WindowHours,
	from apitypes.FromTime,
	to apitypes.ToTime,
	service apitypes.ServiceFilter,
	namespace apitypes.NamespaceFilter,
	status apitypes.StatusFilter,
	limit int,
) (repo.FindingListFilter, error) {
	timeRange, err := findingsTimeRangeFromParams(windowHours, from, to)
	if err != nil {
		return repo.FindingListFilter{}, err
	}
	filter := repo.FindingListInRange(timeRange, limit)
	filter.Service = string(service)
	filter.Namespace = string(namespace)
	filter.Status = string(status)
	return filter, nil
}

func writeBindError(c *gin.Context, err error) {
	writeError(c, http.StatusBadRequest, fmt.Errorf("invalid query: %w", err))
}

// writeError responds with the given status and the standard
// {"error": {code, message}} envelope, deriving a stable code from the status.
func writeError(c *gin.Context, status int, err error) {
	c.JSON(status, apitypes.ErrorResponse{Error: apitypes.ErrorBody{
		Code:    errorCode(status),
		Message: err.Error(),
	}})
}

// errorCode maps an HTTP status to the stable machine-readable code clients
// switch on, independent of the human message.
func errorCode(status int) apitypes.ErrorBodyCode {
	switch status {
	case http.StatusBadRequest:
		return apitypes.ErrorBodyCodeBadRequest
	case http.StatusNotFound:
		return apitypes.ErrorBodyCodeNotFound
	case http.StatusConflict:
		return apitypes.ErrorBodyCodeConflict
	case http.StatusUnprocessableEntity:
		return apitypes.ErrorBodyCodeUnprocessableEntity
	case http.StatusServiceUnavailable:
		return apitypes.ErrorBodyCodeUnavailable
	default:
		return apitypes.ErrorBodyCodeInternalError
	}
}
