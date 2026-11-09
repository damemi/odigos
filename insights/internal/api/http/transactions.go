package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// listTransactions is the GET /api/v1/transactions handler.
func (a *API) listTransactions(c *gin.Context) {
	params, err := bindListTransactionsParams(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := a.ListTransactions(c.Request.Context(), params)
	if err != nil {
		if errors.Is(err, repo.ErrInvalidSort) {
			writeError(c, http.StatusBadRequest, err)
			return
		}
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// getTransaction is the GET /api/v1/transactions/{transaction_id} handler; it
// returns one transaction's identity (service, namespace, operation, kind).
// 404 when the id is unknown.
func (a *API) getTransaction(c *gin.Context) {
	id, ok := parseTransactionID(c)
	if !ok {
		return
	}
	tx, found, err := a.GetTransaction(c.Request.Context(), id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, errors.New("transaction not found"))
		return
	}
	c.JSON(http.StatusOK, apimap.FromTransaction(tx))
}

// deleteTransaction is the DELETE /api/v1/transactions/{transaction_id} handler.
func (a *API) deleteTransaction(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	id, ok := parseTransactionID(c)
	if !ok {
		return
	}
	if err := a.DeleteTransaction(c.Request.Context(), id); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bulkDeleteTransactions is the POST /api/v1/transactions handler; it deletes
// many transactions, tolerating per-id failures and reporting a 422 with the
// count if any failed (same shape as bulkResolveAnomalies).
func (a *API) bulkDeleteTransactions(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.BulkDeleteTransactionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if len(req.TransactionIds) == 0 {
		writeError(c, http.StatusBadRequest, errors.New("no transactions selected"))
		return
	}
	deleted, err := a.DeleteTransactions(c.Request.Context(), req.TransactionIds)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, apitypes.BulkDeleteTransactionsResult{Deleted: deleted})
}

// bulkPromoteTransactions is the POST /api/v1/transactions/promote handler; it
// force-promotes every scored class on many transactions.
func (a *API) bulkPromoteTransactions(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.BulkPromoteTransactionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if len(req.TransactionIds) == 0 {
		writeError(c, http.StatusBadRequest, errors.New("no transactions selected"))
		return
	}
	promoted, err := a.PromoteTransactions(c.Request.Context(), req.TransactionIds)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, apitypes.BulkPromoteTransactionsResult{Promoted: promoted})
}

// getBaselineForTransaction is the GET /api/v1/transactions/{id}/baseline
// handler; it returns all per-class baseline rows for one transaction in a
// single payload, so the UI avoids a fan-out from the browser. Every scored
// class is always represented: a class with no stored row yet (freshly reset,
// or not observed yet) comes back as an empty, unpromoted placeholder so the
// class list stays stable across a re-learn instead of a row disappearing.
func (a *API) getBaselineForTransaction(c *gin.Context) {
	id, ok := parseTransactionID(c)
	if !ok {
		return
	}
	out, err := a.GetTransactionBaseline(c.Request.Context(), id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// promoteBaselineClass is the POST
// /api/v1/transactions/{id}/baseline/{class}/promote handler; it flips
// promoted=true for one (transaction, class) row, skipping the learning-phase
// gates so operators can start scoring a class before its thresholds are met.
func (a *API) promoteBaselineClass(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	txID, ok := parseTransactionID(c)
	if !ok {
		return
	}
	class := repo.DeviationClass(c.Param("class"))
	if class == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing class"))
		return
	}
	if err := a.PromoteBaselineClass(c.Request.Context(), txID, class); err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, apitypes.PromoteResult{
		TransactionId: txID,
		Class:         apitypes.DeviationClass(class),
		Promoted:      true,
	})
}

// resetBaselineClass is the POST
// /api/v1/transactions/{id}/baseline/{class}/reset handler; it wipes one class's
// baseline (via the engine so its cache is invalidated) so the class re-learns
// from the next trace.
func (a *API) resetBaselineClass(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	txID, ok := parseTransactionID(c)
	if !ok {
		return
	}
	class := repo.DeviationClass(c.Param("class"))
	if class == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing class"))
		return
	}
	if err := a.ResetBaselineClass(c.Request.Context(), txID, class); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// resetTransactionBaselines is the POST
// /api/v1/transactions/{id}/baseline/reset-all handler; it wipes every class of
// a transaction so they all re-learn.
func (a *API) resetTransactionBaselines(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	txID, ok := parseTransactionID(c)
	if !ok {
		return
	}
	if err := a.ResetTransactionBaselines(c.Request.Context(), txID); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// promoteTransactionBaselines is the POST
// /api/v1/transactions/{id}/baselines/promote handler; it force-promotes every
// scored class of the transaction.
func (a *API) promoteTransactionBaselines(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	txID, ok := parseTransactionID(c)
	if !ok {
		return
	}
	if err := a.PromoteTransactionBaselines(c.Request.Context(), txID); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// listObservations is the GET /api/v1/transactions/{id}/observations handler; it
// returns trace-example summaries for one transaction, capped at 100 (metadata
// only - no raw_otlp). ?sample_reason=example|anomaly:<signature> filters to one
// bucket. Use getObservation to load a specific blob.
func (a *API) listObservations(c *gin.Context) {
	id, ok := parseTransactionID(c)
	if !ok {
		return
	}
	reason := c.Query("sample_reason")
	obs, err := a.ListObservations(c.Request.Context(), id, reason, 100)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.ObservationList(obs))
}

// getObservation is the GET /api/v1/transactions/{id}/observations/{trace_id}
// handler; it returns one retained sample including raw_otlp. 404 when the
// (transaction, trace) was not retained.
func (a *API) getObservation(c *gin.Context) {
	id, ok := parseTransactionID(c)
	if !ok {
		return
	}
	traceID := c.Param("trace_id")
	if traceID == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing trace_id"))
		return
	}
	obs, found, err := a.GetObservation(c.Request.Context(), id, traceID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, errors.New("observation not found"))
		return
	}
	c.JSON(http.StatusOK, apimap.FromObservation(obs))
}
