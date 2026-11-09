package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// listLearningPolicies is the GET /api/v1/learning-policies handler; it returns
// every learning policy row (global defaults plus scoped overrides) that governs
// how classes graduate from learning to production.
func (a *API) listLearningPolicies(c *gin.Context) {
	policies, err := a.ListLearningPolicies(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.LearningPolicyList(policies))
}

// upsertLearningPolicy is the PUT /api/v1/learning-policies handler; it inserts
// or replaces one class's learning policy (keyed by class + scope + scope_key)
// and reloads the engine snapshot so the promotion gate uses it live.
func (a *API) upsertLearningPolicy(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var body apitypes.LearningPolicy
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if body.Class == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing class"))
		return
	}
	if err := a.UpsertLearningPolicy(c.Request.Context(), apimap.ToLearningPolicy(body)); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// deleteLearningPolicy is the DELETE
// /api/v1/learning-policies?class=&scope=&scope_key= handler; it drops one scoped
// override so the class falls back to a broader policy. Only service/transaction
// overrides are deletable.
func (a *API) deleteLearningPolicy(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	class := c.Query("class")
	if class == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing class"))
		return
	}
	scope, scopeKey, ok := deletableScope(c)
	if !ok {
		return
	}
	if err := a.DeleteLearningPolicy(c.Request.Context(), repo.DeviationClass(class), scope, scopeKey); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// getCatalog is the GET /api/v1/catalog handler; it returns the risk model's
// reference data (deviation classes, enrichers, categories, guardrail rules,
// severity bands, threat tags) so the frontend renders labels and editors
// without hardcoding the model.
func (a *API) getCatalog(c *gin.Context) {
	c.JSON(http.StatusOK, apimap.BuildCatalog())
}
