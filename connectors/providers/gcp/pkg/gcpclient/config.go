// Package gcpclient holds the shared GCP credential/plumbing used by detection: building client
// options from the runtime credentials, verifying the configured project, and constructing the
// per-service API clients (Cloud Run, GKE, Compute Engine, Resource Manager). Keeping this
// provider-internal but detector-agnostic avoids the detector packages depending on each other.
//
// Unlike AWS (which iterates enabled regions), GCP APIs accept a location wildcard ("-"), so a single
// call enumerates every location/zone in the project. The clients here are cheap to construct per
// discovery cycle; callers are responsible for Close-ing them.
package gcpclient

import (
	"context"
	"fmt"

	compute "cloud.google.com/go/compute/apiv1"
	container "cloud.google.com/go/container/apiv1"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	resourcemanagerpb "cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	run "cloud.google.com/go/run/apiv2"
	"google.golang.org/api/option"

	connlog "github.com/odigos-io/odigos/connectors/base/log"
	"github.com/odigos-io/odigos/connectors/providers/gcp/pkg/config"
)

var log = connlog.Subsystem("gcp-client")

// LocationWildcard is the GCP "all locations" selector accepted by list APIs (Cloud Run, GKE) as the
// location segment of the parent resource name.
const LocationWildcard = "-"

// Clients builds the GCP API clients for a project from the runtime credentials. It memoizes nothing
// (the underlying clients hold connections the caller closes), only the resolved project id and the
// client options derived from the credentials.
type Clients struct {
	projectID string
	opts      []option.ClientOption
}

// New builds the client factory for a project over the runtime credentials. When CredentialsJSON is
// empty the ambient Application Default Credentials chain is used.
func New(projectID string, creds config.Credentials) *Clients {
	var opts []option.ClientOption
	if creds.CredentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(creds.CredentialsJSON)))
	}
	return &Clients{projectID: projectID, opts: opts}
}

// ProjectID returns the configured GCP project id (the connector boundary).
func (c *Clients) ProjectID() string { return c.projectID }

// CloudRunParent returns the Cloud Run ListServices parent covering every location in the project.
func (c *Clients) CloudRunParent() string {
	return fmt.Sprintf("projects/%s/locations/%s", c.projectID, LocationWildcard)
}

// GKEParent returns the GKE ListClusters parent covering every location in the project.
func (c *Clients) GKEParent() string {
	return fmt.Sprintf("projects/%s/locations/%s", c.projectID, LocationWildcard)
}

// CloudRun builds a Cloud Run services client. The caller must Close it.
func (c *Clients) CloudRun(ctx context.Context) (*run.ServicesClient, error) {
	return run.NewServicesClient(ctx, c.opts...)
}

// GKEClusters builds a GKE cluster manager client. The caller must Close it.
func (c *Clients) GKEClusters(ctx context.Context) (*container.ClusterManagerClient, error) {
	return container.NewClusterManagerClient(ctx, c.opts...)
}

// GCEInstances builds a Compute Engine instances client (REST transport). The caller must Close it.
func (c *Clients) GCEInstances(ctx context.Context) (*compute.InstancesClient, error) {
	return compute.NewInstancesRESTClient(ctx, c.opts...)
}

// VerifyProject enforces the connector's GCP project boundary: it reads the configured project through
// the runtime credentials (resourcemanager.projects.get) and fails unless it resolves. This proves the
// credentials can access the project the connector was created for before it discovers anything. An
// empty project id is itself a misconfiguration (the boundary cannot be enforced) and is rejected.
func (c *Clients) VerifyProject(ctx context.Context) error {
	if c.projectID == "" {
		return fmt.Errorf("no GCP project id configured (ODIGOS_GCP_PROJECT_ID); cannot enforce the connector project boundary")
	}
	pc, err := resourcemanager.NewProjectsClient(ctx, c.opts...)
	if err != nil {
		return fmt.Errorf("resource manager client: %w", err)
	}
	defer pc.Close()
	proj, err := pc.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/" + c.projectID})
	if err != nil {
		return fmt.Errorf("get project %q: %w", c.projectID, err)
	}
	if proj.GetProjectId() != "" && proj.GetProjectId() != c.projectID {
		return fmt.Errorf("gcp project boundary violation: credentials resolve to project %q but the connector is configured for project %q", proj.GetProjectId(), c.projectID)
	}
	log.Info("gcp project boundary verified", "project", c.projectID)
	return nil
}
