package debugui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

const integrationsPath = "/debug/settings/integrations"

// integrationOption is the compact (id, name, type) a policy/guardrail editor
// needs to render its routing checkboxes.
type integrationOption struct {
	ID   string
	Name string
	Type string
}

// integrationOptions lists integrations for routing pickers. A failure (API
// not wired, DB down) yields an empty list so the editor still renders.
func (u *ui) integrationOptions(ctx context.Context, api ProductAPI) []integrationOption {
	items, err := api.ListIntegrations(ctx)
	if err != nil {
		return nil
	}
	out := make([]integrationOption, len(items))
	for i, in := range items {
		out[i] = integrationOption{ID: in.Id, Name: in.Name, Type: in.Type}
	}
	return out
}

// findingDeliveries loads a finding's ledger rows for the investigate pages;
// best-effort, nil on any failure.
func (u *ui) findingDeliveries(ctx context.Context, key string) []repo.NotificationDispatch {
	api := u.api.Load()
	if api == nil || *api == nil {
		return nil
	}
	rows, err := (*api).FindingDeliveries(ctx, key)
	if err != nil {
		return nil
	}
	return rows
}

// configValue reads one key of a redacted config document for the edit form.
func configValue(raw json.RawMessage, key string) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	switch v := m[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func hasKind(kinds []apitypes.FindingKind, kind string) bool {
	return slices.Contains(kinds, apitypes.FindingKind(kind))
}

// dispatchBadge picks the badge class for a ledger state.
func dispatchBadge(state repo.DispatchState) string {
	switch state {
	case repo.DispatchSent:
		return "badge-ok"
	case repo.DispatchFailed, repo.DispatchPending:
		return "badge-pending"
	default:
		return "badge-warn"
	}
}

func (u *ui) listIntegrations(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	data := map[string]any{"SettingsTab": "integrations"}
	catalog, err := api.IntegrationCatalog()
	if err != nil {
		data["Error"] = err.Error()
		u.render(c, "settings_integrations", "settings", "", data)
		return
	}
	byType := make(map[string]notify.Descriptor, len(catalog))
	for _, d := range catalog {
		byType[d.Type] = d
	}
	items, err := api.ListIntegrations(ctx)
	if err != nil {
		data["Error"] = err.Error()
	}
	type row struct {
		apitypes.Integration
		Descriptor notify.Descriptor
	}
	rows := make([]row, len(items))
	for i, in := range items {
		rows[i] = row{Integration: in, Descriptor: byType[in.Type]}
	}
	data["Catalog"] = catalog
	data["Integrations"] = rows
	data["EmptyIntegration"] = apitypes.Integration{Config: json.RawMessage("{}")}
	data["Severities"] = []string{"", "info", "low", "medium", "high", "critical"}
	u.render(c, "settings_integrations", "settings", c.Query("flash"), data)
}

// saveIntegration creates (no id) or updates (id) an integration from the
// catalog-driven form. Config fields arrive as cfg_<key>; the descriptor says
// how to type them.
func (u *ui) saveIntegration(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	id := strings.TrimSpace(c.PostForm("id"))
	typ := strings.TrimSpace(c.PostForm("type"))
	catalog, err := api.IntegrationCatalog()
	if err != nil {
		redirectError(c, integrationsPath, err.Error())
		return
	}
	var desc notify.Descriptor
	for _, d := range catalog {
		if d.Type == typ {
			desc = d
		}
	}
	if desc.Type == "" {
		redirectError(c, integrationsPath, "unknown integration type "+typ)
		return
	}
	cfg := map[string]any{}
	for _, f := range desc.Fields {
		raw := strings.TrimSpace(c.PostForm("cfg_" + f.Key))
		switch f.Type {
		case notify.FieldBool:
			cfg[f.Key] = raw == "on" || raw == "true" || raw == "1"
		default:
			if raw != "" {
				cfg[f.Key] = raw
			}
		}
	}
	rawCfg, _ := json.Marshal(cfg)
	enabled := c.PostForm("enabled") == "on"
	body := apitypes.IntegrationWrite{
		Name:    strings.TrimSpace(c.PostForm("name")),
		Type:    typ,
		Enabled: &enabled,
		Config:  rawCfg,
		Filters: apitypes.IntegrationFilters{
			MinSeverity:     apitypes.Severity(c.PostForm("min_severity")),
			NotifyOnResolve: c.PostForm("notify_on_resolve") == "on",
		},
	}
	for _, k := range c.PostFormArray("kinds") {
		body.Filters.Kinds = append(body.Filters.Kinds, apitypes.FindingKind(k))
	}
	var out apitypes.Integration
	if id == "" {
		out, err = api.CreateIntegration(ctx, body)
	} else {
		out, err = api.UpdateIntegration(ctx, id, body)
	}
	if err != nil {
		redirectError(c, integrationsPath, "save failed: "+err.Error())
		return
	}
	redirectFlash(c, integrationsPath, out.Name+" saved")
}

func (u *ui) deleteIntegration(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	if err := api.DeleteIntegration(c.Request.Context(), c.PostForm("id")); err != nil {
		redirectError(c, integrationsPath, "delete failed: "+err.Error())
		return
	}
	redirectFlash(c, integrationsPath, "integration deleted")
}

func (u *ui) testIntegration(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	if err := api.TestIntegration(c.Request.Context(), c.PostForm("id")); err != nil {
		redirectError(c, integrationsPath, "test failed: "+err.Error())
		return
	}
	redirectFlash(c, integrationsPath, "test delivery succeeded")
}

func (u *ui) integrationDeliveries(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")
	in, err := api.GetIntegration(ctx, id)
	if err != nil {
		c.String(http.StatusNotFound, "%v", err)
		return
	}
	state := c.Query("state")
	data := map[string]any{
		"SettingsTab": "integrations",
		"Integration": in,
		"State":       state,
		"States":      []string{"", "sent", "failed", "dead", "pending"},
	}
	if rows, err := api.ListIntegrationDeliveries(ctx, id, state, 500); err != nil {
		data["Error"] = err.Error()
	} else {
		data["Deliveries"] = rows
	}
	if attempts, err := api.ListIntegrationAttempts(ctx, id, 24, 200); err != nil {
		data["Error"] = err.Error()
	} else {
		data["Attempts"] = attempts
	}
	u.render(c, "integration_deliveries", "settings", c.Query("flash"), data)
}

func (u *ui) retryIntegrationDeliveries(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	id := c.Param("id")
	back := integrationsPath + "/" + url.PathEscape(id) + "/deliveries"
	keys := c.PostFormArray("finding_key")
	if err := api.RetryIntegrationDeliveries(c.Request.Context(), id, keys); err != nil {
		redirectError(c, back, "retry failed: "+err.Error())
		return
	}
	if len(keys) == 0 {
		redirectFlash(c, back, "all failed and dead deliveries re-armed; they resend on the next tick")
		return
	}
	redirectFlash(c, back, strconv.Itoa(len(keys))+" delivery(ies) re-armed")
}

func redirectError(c *gin.Context, path, msg string) {
	c.Redirect(http.StatusSeeOther, path+"?error="+url.QueryEscape(msg))
}

func redirectFlash(c *gin.Context, path, msg string) {
	c.Redirect(http.StatusSeeOther, path+"?flash="+url.QueryEscape(msg))
}
