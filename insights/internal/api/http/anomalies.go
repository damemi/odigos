package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
)

// listAnomalies is the GET /api/v1/anomalies handler.
func (a *API) listAnomalies(c *gin.Context) {
	params, err := bindListAnomaliesParams(c)
	if err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.ListAnomalies(c.Request.Context(), params)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// getAnomalyIssue is the GET /api/v1/anomalies/{transaction_id}/{signature}
// handler; it returns the investigate payload (evidence, risk, class_findings,
// anomaly/baseline traces). 404 when the signature is unknown.
func (a *API) getAnomalyIssue(c *gin.Context) {
	txID, ok := parseTransactionID(c)
	if !ok {
		return
	}
	signature := c.Param("signature")
	if signature == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing signature"))
		return
	}
	out, found, err := a.GetAnomalyIssueDetail(c.Request.Context(), txID, signature)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, errors.New("anomaly issue not found"))
		return
	}
	c.JSON(http.StatusOK, out)
}

// resolveAnomalyIssue is the POST /api/v1/anomalies/{transaction_id}/{signature}
// handler; it applies one resolution (accept | dismiss | reopen) to a single
// issue. accept folds the trace into the baseline; dismiss closes it unchanged;
// reopen re-triages a dismissed issue. The resolution comes from the body.
func (a *API) resolveAnomalyIssue(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	txID, ok := parseTransactionID(c)
	if !ok {
		return
	}
	signature := c.Param("signature")
	if signature == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing signature"))
		return
	}
	var req apitypes.AnomalyResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	resolution := string(req.Resolution)
	switch resolution {
	case "accept", "dismiss", "reopen":
	default:
		writeError(c, http.StatusBadRequest, errors.New("resolution must be accept, dismiss, or reopen"))
		return
	}
	if err := a.ResolveAnomalyIssue(c.Request.Context(), txID, signature, resolution); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bulkResolveAnomalies is the POST /api/v1/anomalies handler; it applies
// one resolution (accept|dismiss) to many issues, tolerating per-issue failures
// and reporting a 422 with the count if any failed.
func (a *API) bulkResolveAnomalies(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.BulkAnomalyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	resolution := string(req.Resolution)
	if resolution != "accept" && resolution != "dismiss" {
		writeError(c, http.StatusBadRequest, errors.New("resolution must be accept or dismiss"))
		return
	}
	if len(req.Items) == 0 {
		writeError(c, http.StatusBadRequest, errors.New("no items selected"))
		return
	}
	ctx := c.Request.Context()
	var done, failed int
	var lastErr error
	for _, item := range req.Items {
		if item.Signature == "" {
			failed++
			continue
		}
		if err := a.ResolveAnomalyIssue(ctx, item.TransactionId, item.Signature, resolution); err != nil {
			failed++
			lastErr = err
			continue
		}
		done++
	}
	if failed > 0 {
		writeError(c, http.StatusUnprocessableEntity, fmt.Errorf("%s %d, %d failed: %v", resolution, done, failed, lastErr))
		return
	}
	c.JSON(http.StatusOK, apitypes.BulkResolveResult{Resolution: resolution, Resolved: done})
}

// applyAnomalyResolution dispatches one resolution to the matching engine action.
func applyAnomalyResolution(ctx context.Context, admin Admin, resolution string, txID int64, signature string) error {
	switch resolution {
	case "accept":
		return admin.AcceptAnomalyIssue(ctx, txID, signature)
	case "dismiss":
		return admin.DismissAnomalyIssue(ctx, txID, signature)
	case "reopen":
		return admin.ReopenAnomalyIssue(ctx, txID, signature)
	default:
		return fmt.Errorf("unknown resolution %q", resolution)
	}
}
