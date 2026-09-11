package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// listGuardrails is the GET /api/v1/guardrails handler; it returns every
// declared service guardrail (all scopes) with its configured rules.
func (a *API) listGuardrails(c *gin.Context) {
	guardrails, err := a.ListServiceGuardrails(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, apimap.ServiceGuardrailList(guardrails))
}

// listGuardrailViolations is the GET /api/v1/guardrails/violations handler.
func (a *API) listGuardrailViolations(c *gin.Context) {
	params, err := bindListGuardrailViolationsParams(c)
	if err != nil {
		writeBindError(c, err)
		return
	}
	out, err := a.ListGuardrailViolations(c.Request.Context(), params)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// getGuardrailViolation is the GET /api/v1/guardrails/violations/evidence
// handler; it returns the investigate payload (summary, span highlights,
// evidence trace). Identity is (scope_key, rule_key, offending) as query
// params because scope_key contains a slash.
func (a *API) getGuardrailViolation(c *gin.Context) {
	scopeKey := strings.TrimSpace(c.Query("scope_key"))
	ruleKey := repo.RuleKey(strings.TrimSpace(c.Query("rule_key")))
	offending := strings.TrimSpace(c.Query("offending"))
	if scopeKey == "" || ruleKey == "" || offending == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing violation identity (scope_key, rule_key, offending)"))
		return
	}
	out, found, err := a.GetGuardrailViolationDetail(c.Request.Context(), scopeKey, ruleKey, offending)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, errors.New("guardrail violation not found"))
		return
	}
	c.JSON(http.StatusOK, out)
}

// upsertGuardrail is the PUT /api/v1/guardrails handler; it inserts or replaces
// one service's guardrail and reloads the engine's snapshot so enforcement
// applies live. The scope key must be "namespace/service".
func (a *API) upsertGuardrail(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var body apitypes.ServiceGuardrail
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := apimap.ValidateServiceGuardrail(body); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := a.UpsertServiceGuardrail(c.Request.Context(), apimap.ToServiceGuardrail(body)); err != nil {
		if strings.Contains(err.Error(), "namespace/service") {
			writeError(c, http.StatusBadRequest, err)
			return
		}
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// deleteGuardrail is the DELETE /api/v1/guardrails?scope_key=ns/svc handler; it
// removes one service's guardrail and reloads the engine snapshot. scope_key is
// a query param since it contains a slash.
func (a *API) deleteGuardrail(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	if err := a.DeleteServiceGuardrail(c.Request.Context(), repo.ScopeService, c.Query("scope_key")); err != nil {
		if strings.Contains(err.Error(), "missing scope_key") {
			writeError(c, http.StatusBadRequest, err)
			return
		}
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// seedGuardrail is the POST /api/v1/guardrails/seed handler; it adopts observed
// communication (callers/callees/egress) into a service's guardrail - the
// "adopt observed" flow - creating the guardrail if needed, then reloads.
func (a *API) seedGuardrail(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	var req apitypes.GuardrailSeedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := a.SeedServiceGuardrail(c.Request.Context(), req); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "namespace/service") || strings.Contains(msg, "select at least one") {
			writeError(c, http.StatusBadRequest, err)
			return
		}
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func transactionGuardrailScopeFromQuery(c *gin.Context) (namespace, service string, ok bool) {
	namespace = strings.TrimSpace(c.Query("namespace"))
	service = strings.TrimSpace(c.Query("service"))
	if namespace == "" || service == "" {
		writeError(c, http.StatusBadRequest, errors.New("namespace and service are required"))
		return "", "", false
	}
	return namespace, service, true
}

// enableTransactionGuardrail is POST /api/v1/services/transaction-guardrail/enable.
func (a *API) enableTransactionGuardrail(c *gin.Context) {
	admin, ok := a.requireAdmin(c)
	if !ok {
		return
	}
	ns, svc, ok := transactionGuardrailScopeFromQuery(c)
	if !ok {
		return
	}
	if err := admin.EnableTransactionGuardrail(c.Request.Context(), ns, svc); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// disableTransactionGuardrail is POST /api/v1/services/transaction-guardrail/disable.
func (a *API) disableTransactionGuardrail(c *gin.Context) {
	admin, ok := a.requireAdmin(c)
	if !ok {
		return
	}
	ns, svc, ok := transactionGuardrailScopeFromQuery(c)
	if !ok {
		return
	}
	if err := admin.DisableTransactionGuardrail(c.Request.Context(), ns, svc); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// forcePromoteServiceTransactions is POST /api/v1/services/transaction-guardrail/force-promote.
func (a *API) forcePromoteServiceTransactions(c *gin.Context) {
	admin, ok := a.requireAdmin(c)
	if !ok {
		return
	}
	ns, svc, ok := transactionGuardrailScopeFromQuery(c)
	if !ok {
		return
	}
	if err := admin.ForcePromoteService(c.Request.Context(), ns, svc); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// acceptViolation is the POST /api/v1/guardrails/violations/accept handler; it
// declares an offending edge legitimate: it adds the item to the rule's
// allowlist, reloads the engine, then marks the violation accepted (sticky).
func (a *API) acceptViolation(c *gin.Context) {
	if _, ok := a.requireAdmin(c); !ok {
		return
	}
	req, ok := bindViolationAction(c)
	if !ok {
		return
	}
	if err := a.AcceptGuardrailViolation(c.Request.Context(), req.ScopeKey, repo.RuleKey(req.RuleKey), req.Offending); err != nil {
		writeAPIError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// dismissViolation is the POST /api/v1/guardrails/violations/dismiss handler; it
// closes a violation without allowlisting it (repo-only, no engine reload).
func (a *API) dismissViolation(c *gin.Context) {
	a.resolveViolation(c, repo.GuardrailResolveDismiss)
}

// reopenViolation is the POST /api/v1/guardrails/violations/reopen handler; it
// moves a dismissed violation back to open for re-triage.
func (a *API) reopenViolation(c *gin.Context) {
	a.resolveViolation(c, repo.GuardrailResolveReopen)
}

// resolveViolation is the shared repo-only violation resolver for dismiss/reopen.
func (a *API) resolveViolation(c *gin.Context, resolution repo.GuardrailResolution) {
	req, ok := bindViolationAction(c)
	if !ok {
		return
	}
	if err := a.ResolveGuardrailViolation(c.Request.Context(), req.ScopeKey, repo.RuleKey(req.RuleKey), req.Offending, resolution); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bindViolationAction binds and validates a violation-action body.
func bindViolationAction(c *gin.Context) (apitypes.ViolationActionRequest, bool) {
	var req apitypes.ViolationActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return req, false
	}
	if req.ScopeKey == "" || req.RuleKey == "" || req.Offending == "" {
		writeError(c, http.StatusBadRequest, errors.New("missing violation identity (scope_key, rule_key, offending)"))
		return req, false
	}
	return req, true
}

// loadOrInitGuardrail returns the service's existing guardrail, or a fresh
// enforce-mode one if it has none yet.
func (a *API) loadOrInitGuardrail(ctx context.Context, scopeKey string) (repo.ServiceGuardrail, error) {
	guardrails, err := a.Repo.ListServiceGuardrails(ctx)
	if err != nil {
		return repo.ServiceGuardrail{}, err
	}
	for _, g := range guardrails {
		if g.Scope == repo.ScopeService && g.ScopeKey == scopeKey {
			return g, nil
		}
	}
	return repo.ServiceGuardrail{
		Scope:     repo.ScopeService,
		ScopeKey:  scopeKey,
		RuleModes: map[repo.RuleKey]repo.RuleMode{risk.RuleAllowedCallers: repo.RuleModeEnforce},
	}, nil
}

// appendUnique appends v to list only if absent, preserving order.
func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// isServiceScopeKey reports whether s is a "namespace/service" scope key.
func isServiceScopeKey(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && strings.Contains(s, "/")
}
