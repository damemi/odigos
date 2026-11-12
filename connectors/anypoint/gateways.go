package anypoint

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Gateway is an Omni Gateway (formerly Flex Gateway) as Gateway Manager lists it. The field set is what the
// anypoint-cli reads from the same API (unverified beyond that).
type Gateway struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind tells a Managed Omni Gateway (run by MuleSoft on a CloudHub 2.0 space) from a self-managed one.
	Kind           string `json:"kind,omitempty"`
	TargetID       string `json:"targetId,omitempty"`
	TargetName     string `json:"targetName,omitempty"`
	TargetType     string `json:"targetType,omitempty"`
	Status         string `json:"status,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	Ready          *bool  `json:"ready,omitempty"`
}

// Managed reports whether MuleSoft runs the gateway, which is what makes its configuration writable through
// Gateway Manager. A self-managed gateway keeps its configuration on the customer's host.
func (g Gateway) Managed() bool {
	k := strings.ToLower(g.Kind)
	switch {
	case strings.Contains(k, "self"):
		return false
	case strings.Contains(k, "managed"):
		return true
	}
	// No kind: the managed listing is the only one that names a CloudHub 2.0 space as the target.
	return k == "" && g.TargetID != ""
}

// GatewayDetail is a gateway's full Gateway Manager record, kept as raw JSON. A write sends the configuration
// back whole, so every field this client does not model survives it.
type GatewayDetail struct {
	raw map[string]any
}

// Summary is the detail's listing fields.
func (d GatewayDetail) Summary() Gateway {
	var g Gateway
	b, _ := json.Marshal(d.raw)
	_ = json.Unmarshal(b, &g)
	return g
}

// Tracing returns a copy of configuration.tracing, or nil when the gateway has none.
func (d GatewayDetail) Tracing() map[string]any {
	cfg, _ := d.raw["configuration"].(map[string]any)
	t, _ := cfg["tracing"].(map[string]any)
	if t == nil {
		return nil
	}
	return deepCopy(t)
}

// UpdateBody is the body Gateway Manager's update takes — the create body: name, target, runtime version,
// release channel, size and configuration — with configuration.tracing replaced by tracing (nil removes it).
func (d GatewayDetail) UpdateBody(tracing map[string]any) map[string]any {
	body := map[string]any{}
	for _, k := range []string{"name", "targetId", "runtimeVersion", "releaseChannel", "size"} {
		if v, ok := d.raw[k]; ok {
			body[k] = v
		}
	}
	cfg, _ := deepCopy(d.raw)["configuration"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	if tracing == nil {
		delete(cfg, "tracing")
	} else {
		cfg["tracing"] = deepCopy(tracing)
	}
	body["configuration"] = cfg
	return body
}

// NewGatewayDetail wraps a raw record. For tests and the fake.
func NewGatewayDetail(raw map[string]any) GatewayDetail { return GatewayDetail{raw: deepCopy(raw)} }

func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func gatewaysXAPIPath(orgID, envID string) string {
	return "/gatewaymanager/xapi/v1/organizations/" + url.PathEscape(orgID) + "/environments/" + url.PathEscape(envID) + "/gateways"
}

func gatewaysAPIPath(orgID, envID string) string {
	return "/gatewaymanager/api/v1/organizations/" + url.PathEscape(orgID) + "/environments/" + url.PathEscape(envID) + "/gateways"
}

// ListGateways lists the Omni Gateways Gateway Manager knows in an environment. A response shorter than its
// reported total returns the items with ErrIncomplete.
func (c *Client) ListGateways(ctx context.Context, orgID, envID string) ([]Gateway, error) {
	var out struct {
		Content       []Gateway `json:"content"`
		TotalElements *int      `json:"totalElements"`
	}
	path := gatewaysXAPIPath(orgID, envID)
	if err := c.do(ctx, request{method: http.MethodGet, path: path}, &out); err != nil {
		return nil, err
	}
	return out.Content, checkComplete(path, len(out.Content), out.TotalElements)
}

// GetGateway returns one gateway's full record, configuration included.
func (c *Client) GetGateway(ctx context.Context, orgID, envID, gatewayID string) (GatewayDetail, error) {
	var raw map[string]any
	path := gatewaysXAPIPath(orgID, envID) + "/" + url.PathEscape(gatewayID)
	if err := c.do(ctx, request{method: http.MethodGet, path: path}, &raw); err != nil {
		return GatewayDetail{}, err
	}
	if raw == nil {
		return GatewayDetail{}, fmt.Errorf("GET %s: empty gateway record", path)
	}
	return GatewayDetail{raw: raw}, nil
}

// UpdateGateway replaces a managed gateway's settings with body (see GatewayDetail.UpdateBody). Gateway
// Manager rolls the gateway to apply a configuration change.
func (c *Client) UpdateGateway(ctx context.Context, orgID, envID, gatewayID string, body map[string]any) error {
	path := gatewaysAPIPath(orgID, envID) + "/" + url.PathEscape(gatewayID)
	return c.do(ctx, request{method: http.MethodPut, path: path, body: body}, nil)
}
