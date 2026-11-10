package anypoint

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// Me returns the Connected App's identity. The response shape differs between user and client tokens, so both
// are read: a client-credentials token reports its organization under "client" (unverified), a user token
// under "user.organization".
func (c *Client) Me(ctx context.Context) (Identity, error) {
	var out struct {
		Client *struct {
			Name  string `json:"name"`
			OrgID string `json:"org_id"`
		} `json:"client"`
		User *struct {
			Username     string `json:"username"`
			Organization *struct {
				ID string `json:"id"`
			} `json:"organization"`
		} `json:"user"`
	}
	if err := c.do(ctx, request{method: http.MethodGet, path: "/accounts/api/me"}, &out); err != nil {
		return Identity{}, err
	}
	var id Identity
	if out.Client != nil {
		id.OrgID, id.ClientName = out.Client.OrgID, out.Client.Name
	}
	if id.OrgID == "" && out.User != nil && out.User.Organization != nil {
		id.OrgID, id.ClientName = out.User.Organization.ID, out.User.Username
	}
	if id.OrgID == "" {
		return id, fmt.Errorf("GET /accounts/api/me: no organization id in the response")
	}
	return id, nil
}

// Environments lists the environments of an organization that the Connected App can see. A response shorter
// than its reported total returns the items with ErrIncomplete.
func (c *Client) Environments(ctx context.Context, orgID string) ([]Environment, error) {
	var out struct {
		Data  []Environment `json:"data"`
		Total *int          `json:"total"`
	}
	path := "/accounts/api/organizations/" + url.PathEscape(orgID) + "/environments"
	if err := c.do(ctx, request{method: http.MethodGet, path: path}, &out); err != nil {
		return nil, err
	}
	return out.Data, checkComplete(path, len(out.Data), out.Total)
}

// Targets lists the organization's deployment targets: CloudHub 2.0 shared and private spaces and Runtime
// Fabrics (unverified path).
func (c *Client) Targets(ctx context.Context, orgID string) ([]Target, error) {
	var out []Target
	path := "/runtimefabric/api/organizations/" + url.PathEscape(orgID) + "/targets"
	if err := c.do(ctx, request{method: http.MethodGet, path: path}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func deploymentsPath(orgID, envID string) string {
	return "/amc/application-manager/api/v2/organizations/" + url.PathEscape(orgID) +
		"/environments/" + url.PathEscape(envID) + "/deployments"
}

// ListDeployments lists the AMC deployments (CloudHub 2.0 and Runtime Fabric) in an environment. The list
// view is a summary; GetDeployment returns the properties and runtime settings. A response shorter than its
// reported total returns the items with ErrIncomplete.
func (c *Client) ListDeployments(ctx context.Context, orgID, envID string) ([]Deployment, error) {
	var out struct {
		Items []Deployment `json:"items"`
		Total *int         `json:"total"`
	}
	path := deploymentsPath(orgID, envID)
	if err := c.do(ctx, request{method: http.MethodGet, path: path}, &out); err != nil {
		return nil, err
	}
	return out.Items, checkComplete(path, len(out.Items), out.Total)
}

// GetDeployment returns one AMC deployment in full.
func (c *Client) GetDeployment(ctx context.Context, orgID, envID, deploymentID string) (Deployment, error) {
	var out Deployment
	path := deploymentsPath(orgID, envID) + "/" + url.PathEscape(deploymentID)
	if err := c.do(ctx, request{method: http.MethodGet, path: path}, &out); err != nil {
		return Deployment{}, err
	}
	return out, nil
}

// UpdateDeploymentProperties replaces a deployment's (non-secure) application properties. The platform rolls
// the application to apply them, which RESTARTS it: callers must only call this when the properties differ.
//
// The whole map is sent, not a delta, because the properties object is replaced as a unit (unverified); a
// partial map would delete every property the caller left out. Secure properties are not sent, so the
// platform keeps them as they are.
func (c *Client) UpdateDeploymentProperties(ctx context.Context, orgID, envID, deploymentID, appName string, props map[string]string) error {
	if props == nil {
		props = map[string]string{}
	}
	body := map[string]any{
		"application": map[string]any{
			"configuration": map[string]any{
				PropertiesServiceKey: PropertiesService{ApplicationName: appName, Properties: props},
			},
		},
	}
	path := deploymentsPath(orgID, envID) + "/" + url.PathEscape(deploymentID)
	return c.do(ctx, request{method: http.MethodPatch, path: path, body: body}, nil)
}

// ListHybridApplications lists the applications deployed to customer-managed servers in an environment.
func (c *Client) ListHybridApplications(ctx context.Context, orgID, envID string) ([]HybridApplication, error) {
	var out struct {
		Data  []HybridApplication `json:"data"`
		Total *int                `json:"total"`
	}
	r := request{method: http.MethodGet, path: "/hybrid/api/v1/applications", headers: orgEnvHeaders(orgID, envID)}
	if err := c.do(ctx, r, &out); err != nil {
		return nil, err
	}
	return out.Data, checkComplete(r.path, len(out.Data), out.Total)
}

// ListHybridServers lists the Runtime Manager-registered servers in an environment (unverified path).
func (c *Client) ListHybridServers(ctx context.Context, orgID, envID string) ([]HybridServer, error) {
	var out struct {
		Data []HybridServer `json:"data"`
	}
	r := request{method: http.MethodGet, path: "/hybrid/api/v1/servers", headers: orgEnvHeaders(orgID, envID)}
	if err := c.do(ctx, r, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
