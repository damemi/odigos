// Package fake is an in-memory Anypoint Platform API serving exactly the endpoints package anypoint calls. Unit
// tests run against it through httptest, and cmd/fake-anypoint serves it so a connector in kind can discover
// and instrument "Mule apps" end to end without a MuleSoft organization. Telemetry does not flow — nothing
// runs the apps — but every state transition the connector reports does.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"github.com/odigos-io/odigos/connectors/anypoint"
)

// State is the platform content the fake serves. It is also the JSON document cmd/fake-anypoint loads.
type State struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	OrgID        string `json:"orgId"`
	ClientName   string `json:"clientName"`

	Environments []anypoint.Environment `json:"environments"`
	Targets      []anypoint.Target      `json:"targets"`
	// Deployments, HybridApps and HybridServers are keyed by environment id.
	Deployments   map[string][]anypoint.Deployment        `json:"deployments"`
	HybridApps    map[string][]anypoint.HybridApplication `json:"hybridApps"`
	HybridServers map[string][]anypoint.HybridServer      `json:"hybridServers"`
	// APIs (API Manager instances) and Gateways (Gateway Manager records, raw as the API returns them) are
	// keyed by environment id too.
	APIs     map[string][]anypoint.APIInstance `json:"apis,omitempty"`
	Gateways map[string][]map[string]any       `json:"gateways,omitempty"`

	// Forbidden lists path prefixes answered with 403, simulating a Connected App missing a scope.
	Forbidden []string `json:"forbidden,omitempty"`
}

// GatewayUpdate records one gateway update the fake received.
type GatewayUpdate struct {
	EnvID     string
	GatewayID string
	Body      map[string]any
}

// Patch records one properties update the fake received.
type Patch struct {
	EnvID        string
	DeploymentID string
	Properties   map[string]string
}

// Server is a running fake.
type Server struct {
	*httptest.Server
	h *Handler
}

// NewServer starts a fake over a copy of state.
func NewServer(state State) *Server {
	h := NewHandler(state)
	return &Server{Server: httptest.NewServer(h), h: h}
}

// Handler is the fake's http.Handler; exported so cmd/fake-anypoint can serve it on a real listener.
type Handler struct {
	mu    sync.Mutex
	state State

	// SettleOnPatch makes a patched deployment report APPLIED immediately. When false, it reports APPLYING
	// until Settle is called — what a real rolling update looks like to a connector polling it.
	SettleOnPatch bool
	// ThrottleNext answers the next N authenticated requests with 429, to exercise client backoff.
	ThrottleNext int
	// InflateTotals adds this many to every list's reported total, imitating a truncated (paged) response.
	InflateTotals int
	// DropTracingProvider makes a gateway update keep everything but configuration.tracing.provider: an
	// Anypoint release that does not accept a custom tracing provider through the API.
	DropTracingProvider bool
	// TrialAccount rejects turning gateway tracing on, as Anypoint does for a trial organization.
	TrialAccount bool

	patches        []Patch
	gatewayUpdates []GatewayUpdate
	tokenIssued    int
}

// NewHandler builds a handler over a deep-enough copy of state that callers can keep mutating their value.
func NewHandler(state State) *Handler {
	b, _ := json.Marshal(state)
	var cp State
	_ = json.Unmarshal(b, &cp)
	if cp.Deployments == nil {
		cp.Deployments = map[string][]anypoint.Deployment{}
	}
	return &Handler{state: cp, SettleOnPatch: true}
}

// Handler exposes the underlying handler of a Server for knobs and inspection.
func (s *Server) Handler() *Handler { return s.h }

// Patches returns the property updates received so far.
func (h *Handler) Patches() []Patch {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Patch(nil), h.patches...)
}

// GatewayUpdates returns the gateway updates received so far.
func (h *Handler) GatewayUpdates() []GatewayUpdate {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]GatewayUpdate(nil), h.gatewayUpdates...)
}

// Gateway returns one gateway's current record.
func (h *Handler) Gateway(envID, id string) (map[string]any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, g := range h.state.Gateways[envID] {
		if g["id"] == id {
			b, _ := json.Marshal(g)
			var cp map[string]any
			_ = json.Unmarshal(b, &cp)
			return cp, true
		}
	}
	return nil, false
}

// SetGateway replaces (or adds) one gateway's record, as a customer editing it in Runtime Manager would.
func (h *Handler) SetGateway(envID string, record map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	gs := h.state.Gateways[envID]
	for i := range gs {
		if gs[i]["id"] == record["id"] {
			gs[i] = record
			return
		}
	}
	if h.state.Gateways == nil {
		h.state.Gateways = map[string][]map[string]any{}
	}
	h.state.Gateways[envID] = append(gs, record)
}

// TokensIssued returns how many access tokens the fake has issued.
func (h *Handler) TokensIssued() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tokenIssued
}

// Settle marks every in-flight deployment APPLIED and RUNNING.
func (h *Handler) Settle() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for env, ds := range h.state.Deployments {
		for i := range ds {
			if ds[i].Status != anypoint.DeploymentApplied && ds[i].Status != anypoint.DeploymentFailed {
				ds[i].Status = anypoint.DeploymentApplied
				ds[i].Application.Status = "RUNNING"
			}
		}
		h.state.Deployments[env] = ds
	}
	for _, gs := range h.state.Gateways {
		for _, g := range gs {
			if g["status"] == "APPLYING" {
				g["status"], g["ready"] = "RUNNING", true
			}
		}
	}
}

// Deployment returns the fake's current copy of a deployment.
func (h *Handler) Deployment(envID, id string) (anypoint.Deployment, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, d := range h.state.Deployments[envID] {
		if d.ID == id {
			return d, true
		}
	}
	return anypoint.Deployment{}, false
}

// SetDeployment replaces (or adds) a deployment in an environment.
func (h *Handler) SetDeployment(envID string, d anypoint.Deployment) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ds := h.state.Deployments[envID]
	for i := range ds {
		if ds[i].ID == d.ID {
			ds[i] = d
			h.state.Deployments[envID] = ds
			return
		}
	}
	h.state.Deployments[envID] = append(ds, d)
}

const fakeToken = "fake-access-token"

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/accounts/api/v2/oauth2/token" {
		h.token(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "invalid token"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ThrottleNext > 0 {
		h.ThrottleNext--
		w.Header().Set("Retry-After", "0")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "rate limited"})
		return
	}
	for _, p := range h.state.Forbidden {
		if strings.HasPrefix(r.URL.Path, p) {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "You do not have permission to access this resource"})
			return
		}
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/accounts/api/me":
		writeJSON(w, http.StatusOK, map[string]any{"client": map[string]string{"name": h.state.ClientName, "org_id": h.state.OrgID}})
	case len(parts) == 5 && parts[0] == "accounts" && parts[3] == h.state.OrgID && parts[4] == "environments":
		writeJSON(w, http.StatusOK, map[string]any{"data": h.state.Environments, "total": len(h.state.Environments) + h.InflateTotals})
	case len(parts) == 5 && parts[0] == "runtimefabric" && parts[3] == h.state.OrgID && parts[4] == "targets":
		writeJSON(w, http.StatusOK, h.state.Targets)
	case len(parts) >= 9 && parts[0] == "amc" && parts[5] == h.state.OrgID && parts[8] == "deployments":
		h.deployments(w, r, parts[7], parts[9:])
	case r.URL.Path == "/hybrid/api/v1/applications":
		writeJSON(w, http.StatusOK, map[string]any{"data": h.state.HybridApps[r.Header.Get("X-ANYPNT-ENV-ID")]})
	case r.URL.Path == "/hybrid/api/v1/servers":
		writeJSON(w, http.StatusOK, map[string]any{"data": h.state.HybridServers[r.Header.Get("X-ANYPNT-ENV-ID")]})
	case len(parts) == 8 && parts[0] == "apimanager" && parts[1] == "api" && parts[4] == h.state.OrgID && parts[7] == "apis":
		h.apis(w, r, parts[6])
	case len(parts) >= 8 && parts[0] == "gatewaymanager" && parts[4] == h.state.OrgID && parts[7] == "gateways":
		h.gateways(w, r, parts[1], parts[6], parts[8:])
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found: " + r.URL.Path})
	}
}

func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	if r.Form.Get("client_id") != h.state.ClientID || r.Form.Get("client_secret") != h.state.ClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "Invalid client credentials"})
		return
	}
	h.mu.Lock()
	h.tokenIssued++
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"access_token": fakeToken, "token_type": "bearer", "expires_in": 3600})
}

func (h *Handler) deployments(w http.ResponseWriter, r *http.Request, envID string, rest []string) {
	ds := h.state.Deployments[envID]
	if len(rest) == 0 {
		// The list view is a summary: it omits the application configuration, like the real API.
		items := make([]anypoint.Deployment, 0, len(ds))
		for _, d := range ds {
			d.Application.Configuration = nil
			items = append(items, d)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items) + h.InflateTotals})
		return
	}
	id := rest[0]
	idx := -1
	for i := range ds {
		if ds[i].ID == id {
			idx = i
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "deployment not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, ds[idx])
	case http.MethodPatch:
		var body struct {
			Application struct {
				Configuration map[string]anypoint.PropertiesService `json:"configuration"`
			} `json:"application"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		ps, ok := body.Application.Configuration[anypoint.PropertiesServiceKey]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "no properties service in patch"})
			return
		}
		d := ds[idx]
		if d.Application.Configuration == nil {
			d.Application.Configuration = &anypoint.ApplicationConfiguration{}
		}
		prev := d.Application.Configuration.PropertiesService
		next := &anypoint.PropertiesService{ApplicationName: ps.ApplicationName, Properties: ps.Properties}
		if prev != nil {
			// Secure properties survive a patch that omits them, as on the platform.
			next.SecureProperties = prev.SecureProperties
		}
		d.Application.Configuration.PropertiesService = next
		if h.SettleOnPatch {
			d.Status = anypoint.DeploymentApplied
		} else {
			d.Status = "APPLYING"
		}
		ds[idx] = d
		h.state.Deployments[envID] = ds
		props := map[string]string{}
		for k, v := range ps.Properties {
			props[k] = v
		}
		h.patches = append(h.patches, Patch{EnvID: envID, DeploymentID: id, Properties: props})
		writeJSON(w, http.StatusOK, d)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}

// apis serves API Manager's instance listing: instances grouped under their Exchange asset, paged by instance.
func (h *Handler) apis(w http.ResponseWriter, r *http.Request, envID string) {
	all := h.state.APIs[envID]
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = len(all)
	}
	var page []anypoint.APIInstance
	if offset < len(all) {
		page = all[offset:min(len(all), offset+limit)]
	}
	type wireAPI struct {
		ID             int64  `json:"id"`
		AssetID        string `json:"assetId"`
		AssetVersion   string `json:"assetVersion"`
		ProductVersion string `json:"productVersion"`
		InstanceLabel  string `json:"instanceLabel"`
		Technology     string `json:"technology"`
	}
	type wireAsset struct {
		AssetID           string    `json:"assetId"`
		ExchangeAssetName string    `json:"exchangeAssetName"`
		APIs              []wireAPI `json:"apis"`
	}
	var assets []*wireAsset
	byAsset := map[string]*wireAsset{}
	for _, a := range page {
		as := byAsset[a.AssetID]
		if as == nil {
			as = &wireAsset{AssetID: a.AssetID, ExchangeAssetName: a.AssetName}
			byAsset[a.AssetID] = as
			assets = append(assets, as)
		}
		id, _ := strconv.ParseInt(a.ID, 10, 64) // a number on the wire, as on the platform
		as.APIs = append(as.APIs, wireAPI{ID: id, AssetID: a.AssetID, AssetVersion: a.AssetVersion,
			ProductVersion: a.ProductVersion, InstanceLabel: a.Label, Technology: a.Technology})
	}
	if assets == nil {
		assets = []*wireAsset{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(all), "assets": assets})
}

// gateways serves Gateway Manager: the xapi listing and detail, and the api update of a managed gateway.
func (h *Handler) gateways(w http.ResponseWriter, r *http.Request, flavor, envID string, rest []string) {
	gs := h.state.Gateways[envID]
	if len(rest) == 0 && flavor == "xapi" && r.Method == http.MethodGet {
		// The listing is a summary, as on a live organization: no configuration, and no runtime version (only
		// the detail record carries it).
		content := make([]map[string]any, 0, len(gs))
		for _, g := range gs {
			summary := map[string]any{}
			for k, v := range g {
				if k != "configuration" && k != "runtimeVersion" {
					summary[k] = v
				}
			}
			content = append(content, summary)
		}
		writeJSON(w, http.StatusOK, map[string]any{"content": content, "totalElements": len(content) + h.InflateTotals})
		return
	}
	if len(rest) != 1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found: " + r.URL.Path})
		return
	}
	idx := -1
	for i := range gs {
		if gs[i]["id"] == rest[0] {
			idx = i
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "gateway not found"})
		return
	}
	switch {
	case flavor == "xapi" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, gs[idx])
	case flavor == "api" && r.Method == http.MethodPut:
		if !strings.Contains(strings.ToLower(fmt.Sprint(gs[idx]["kind"])), "managed") || strings.Contains(strings.ToLower(fmt.Sprint(gs[idx]["kind"])), "self") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "only a managed gateway can be updated here"})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		cfg, _ := body["configuration"].(map[string]any)
		if t, ok := cfg["tracing"].(map[string]any); ok && h.TrialAccount && t["enabled"] == true {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "The deployment configuration violates Trial Account restrictions: configuration.tracing.enabled: expected false, received true"})
			return
		}
		if t, ok := cfg["tracing"].(map[string]any); ok && h.DropTracingProvider {
			delete(t, "provider")
		}
		g := gs[idx]
		for k, v := range body {
			g[k] = v
		}
		// A healthy managed gateway reports RUNNING (live API); one mid-update, a transitional status and not ready.
		if h.SettleOnPatch {
			g["status"], g["ready"] = "RUNNING", true
		} else {
			g["status"], g["ready"] = "APPLYING", false
		}
		gs[idx] = g
		h.state.Gateways[envID] = gs
		h.gatewayUpdates = append(h.gatewayUpdates, GatewayUpdate{EnvID: envID, GatewayID: rest[0], Body: body})
		writeJSON(w, http.StatusOK, g)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}
