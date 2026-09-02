package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// listPolicies is the GET /api/v1/policies handler; it returns every alert
// policy (enabled and disabled) so the settings UI can show and re-enable
// disabled rows. Runtime alert evaluation still filters to enabled policies
// inside the engine.
func (a *API) listPolicies(c *gin.Context) {
	policies, err := a.ListAlertPolicies(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.PolicyList(policies))
}

// upsertPolicy is the PUT /api/v1/policies handler; it inserts or replaces one
// scoped alert policy (keyed by scope + scope_key) and reloads the engine
// snapshot so it fires live.
func (a *API) upsertPolicy(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var body apitypes.Policy
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := a.UpsertAlertPolicy(c.Request.Context(), apimap.ToPolicy(body)); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// deletePolicy is the DELETE /api/v1/policies?scope=&scope_key= handler; it drops
// one scoped override so the scope falls back to a broader policy. Only
// service/transaction overrides are deletable (the global default must exist).
func (a *API) deletePolicy(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	scope, scopeKey, ok := deletableScope(c)
	if !ok {
		return
	}
	if err := a.DeleteAlertPolicy(c.Request.Context(), scope, scopeKey); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// deletableScope reads and validates the ?scope=&scope_key= of a delete-override
// request, rejecting the global scope (which has no override to remove).
func deletableScope(c *gin.Context) (repo.PolicyScope, string, bool) {
	scope := repo.NormalizePolicyScope(repo.PolicyScope(c.Query("scope")))
	scopeKey := c.Query("scope_key")
	if scope == repo.ScopeGlobal {
		writeError(c, http.StatusUnprocessableEntity, errors.New("only service/transaction overrides can be deleted"))
		return "", "", false
	}
	return scope, scopeKey, true
}
