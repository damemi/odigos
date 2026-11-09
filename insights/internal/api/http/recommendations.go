package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
)

// listRecommendations is GET /api/v1/recommendations.
func (a *API) listRecommendations(c *gin.Context) {
	var params apitypes.ListRecommendationsParams
	if err := c.ShouldBindQuery(&params); err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.ListRecommendations(c.Request.Context(), params)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// getRecommendation is GET /api/v1/recommendations/{recommendation_id}.
func (a *API) getRecommendation(c *gin.Context) {
	out, found, err := a.GetRecommendation(c.Request.Context(), c.Param("recommendation_id"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, errors.New("recommendation not found"))
		return
	}
	c.JSON(http.StatusOK, out)
}

// applyRecommendations is POST /api/v1/recommendations/apply.
func (a *API) applyRecommendations(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.RecommendationApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := a.ApplyRecommendations(c.Request.Context(), req)
	writeRecommendationBulk(c, out, err)
}

// dismissRecommendations is POST /api/v1/recommendations/dismiss.
func (a *API) dismissRecommendations(c *gin.Context) {
	a.recommendationIDsAction(c, a.DismissRecommendations)
}

// restoreRecommendations is POST /api/v1/recommendations/restore.
func (a *API) restoreRecommendations(c *gin.Context) {
	a.recommendationIDsAction(c, a.RestoreRecommendations)
}

// revertRecommendations is POST /api/v1/recommendations/revert.
func (a *API) revertRecommendations(c *gin.Context) {
	a.recommendationIDsAction(c, a.RevertRecommendations)
}

type recommendationIDsFunc func(ctx context.Context, ids []string) (apitypes.RecommendationBulkResult, error)

func (a *API) recommendationIDsAction(c *gin.Context, do recommendationIDsFunc) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.RecommendationIDsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := do(c.Request.Context(), req.Ids)
	writeRecommendationBulk(c, out, err)
}

// writeRecommendationBulk answers 200 when every item succeeded and 422 with
// the per-item errors when some did; request-level errors go through
// writeAPIError.
func writeRecommendationBulk(c *gin.Context, out apitypes.RecommendationBulkResult, err error) {
	if err != nil {
		writeAPIError(c, err)
		return
	}
	if out.Failed > 0 {
		c.JSON(http.StatusUnprocessableEntity, out)
		return
	}
	c.JSON(http.StatusOK, out)
}

// previewRecommendation is POST /api/v1/recommendations/preview.
func (a *API) previewRecommendation(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.RecommendationPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	out, err := a.PreviewRecommendation(c.Request.Context(), req)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// recomputeRecommendations is POST /api/v1/recommendations/recompute. The body
// is optional; an empty one sweeps every promoted transaction and service.
func (a *API) recomputeRecommendations(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.RecommendationRecomputeRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			writeError(c, http.StatusBadRequest, err)
			return
		}
	}
	if err := a.RecomputeRecommendations(c.Request.Context(), req); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}
