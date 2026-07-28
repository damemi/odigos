package testconnection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	container "cloud.google.com/go/container/apiv1"
	containerpb "cloud.google.com/go/container/apiv1/containerpb"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	resourcemanagerpb "cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	run "cloud.google.com/go/run/apiv2"
	runpb "cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Discovery type names, matching the discovery types declared in connectors/spec/data/gcp.yaml.
const (
	typeCloudRun = "gcp.cloud-run"
	typeGKE      = "gcp.gke"
	typeGCE      = "gcp.gce"
)

// Credential keys, matching the auth field names declared in connectors/spec/data/gcp.yaml.
const credCredentialsJSON = "credentialsJson"

// GCP connection check names.
const (
	CheckGCPAuthIdentity      CheckName = "auth_identity"
	CheckGCPProjectMatch      CheckName = "project_match"
	CheckGCPDiscoveryCloudRun CheckName = "discovery_cloud_run"
	CheckGCPDiscoveryGKE      CheckName = "discovery_gke"
	CheckGCPDiscoveryGCE      CheckName = "discovery_gce"
)

func init() { Register(gcpTester{}) }

// gcpTester validates GCP connector credentials with read-only probes (project access + discovery
// permissions) using in-memory credentials only.
type gcpTester struct{}

func (gcpTester) Provider() string { return "gcp" }

// Test executes the read-only checks and returns a per-check report. It returns a non-nil error only
// for unexpected internal failures; expected outcomes (bad credentials, missing permissions) are
// reported as failed checks so the UI can render per-check guidance.
func (gcpTester) Test(ctx context.Context, in Input) (*Result, error) {
	res := &Result{}
	gcpSpec := spec.MustGet("gcp")

	// Resolve which auth method the provided credentials select, validated against the spec's auth
	// definition. This rejects incomplete/ambiguous credential sets before contacting GCP.
	chain, err := gcpSpec.Auth.Resolve(in.Credentials)
	if err != nil {
		res.add(CheckResult{
			Name: CheckGCPAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     err.Error(),
			Remediation: "provide a valid set of credentials for one of the supported authentication methods",
		})
		res.finalize()
		return res, nil
	}

	opts := clientOptions(in.Credentials)

	// 1. Authentication + project access (resourcemanager.projects.get). GCP has no cross-account
	// identity probe like AWS STS; reading the configured project both proves the credentials work and
	// validates the project boundary.
	projectID := in.ExpectedAccountID
	if projectID == "" {
		res.add(CheckResult{
			Name: CheckGCPAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     "no GCP project id configured",
			Remediation: "set the connector's project id",
		})
		res.finalize()
		return res, nil
	}
	pc, err := resourcemanager.NewProjectsClient(ctx, opts...)
	if err != nil {
		res.add(CheckResult{
			Name: CheckGCPAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     "failed to initialize GCP credentials",
			Remediation: "verify the provided service account key JSON",
		})
		res.finalize()
		return res, nil
	}
	defer pc.Close()

	proj, err := pc.GetProject(ctx, &resourcemanagerpb.GetProjectRequest{Name: "projects/" + projectID})
	if err != nil {
		res.add(CheckResult{
			Name: CheckGCPAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     summarizeGCPErr(err),
			Remediation: "ensure the credentials are valid and granted resourcemanager.projects.get on the project",
		})
		res.finalize()
		return res, nil
	}
	res.AccountID = proj.GetProjectId()
	res.Identity = proj.GetDisplayName()
	res.add(CheckResult{
		Name: CheckGCPAuthIdentity, DisplayName: "Authentication", Passed: true,
		Message: fmt.Sprintf("authenticated to project %s (via %s)", proj.GetProjectId(), authMethodNames(chain)),
	})

	// 2. Project match.
	match := res.AccountID == "" || res.AccountID == projectID
	c := CheckResult{Name: CheckGCPProjectMatch, DisplayName: "Project match", Passed: match}
	if match {
		c.Message = fmt.Sprintf("credentials can access project %s", projectID)
	} else {
		c.Message = fmt.Sprintf("credentials resolve to project %s, expected %s", res.AccountID, projectID)
		c.Remediation = "use credentials for the configured project, or correct the configured project id"
	}
	res.add(c)

	// Only probe discovery if the customer enabled at least one discovery capability.
	active := map[string]bool{}
	for _, t := range gcpSpec.ActiveDiscoveryTypes(in.Capabilities) {
		active[t.Name] = true
	}
	if len(active) == 0 {
		res.finalize()
		return res, nil
	}

	if active[typeCloudRun] {
		err := probeCloudRun(ctx, projectID, opts)
		res.add(boolCheck(CheckGCPDiscoveryCloudRun, "Cloud Run discovery permissions", !isGCPAuthErr(err),
			"run.services.list allowed",
			"access denied listing Cloud Run services: "+summarizeGCPErr(err),
			"grant "+permissionsFor(gcpSpec, in.Capabilities, "run.")))
	}
	if active[typeGKE] {
		err := probeGKE(ctx, projectID, opts)
		res.add(boolCheck(CheckGCPDiscoveryGKE, "GKE discovery permissions", !isGCPAuthErr(err),
			"container.clusters.list allowed",
			"access denied listing GKE clusters: "+summarizeGCPErr(err),
			"grant "+permissionsFor(gcpSpec, in.Capabilities, "container.")))
	}
	if active[typeGCE] {
		err := probeGCE(ctx, projectID, opts)
		res.add(boolCheck(CheckGCPDiscoveryGCE, "Compute Engine discovery permissions", !isGCPAuthErr(err),
			"compute.instances.list allowed",
			"access denied listing Compute Engine instances: "+summarizeGCPErr(err),
			"grant "+permissionsFor(gcpSpec, in.Capabilities, "compute.")))
	}

	res.finalize()
	return res, nil
}

// clientOptions builds the GCP client options from the in-memory credentials. Empty credentials use the
// ambient Application Default Credentials chain (workload identity).
func clientOptions(creds map[string]string) []option.ClientOption {
	if j := creds[credCredentialsJSON]; j != "" {
		return []option.ClientOption{option.WithCredentialsJSON([]byte(j))}
	}
	return nil
}

// probeCloudRun issues a single ListServices call (the minimal read the discovery loop performs).
func probeCloudRun(ctx context.Context, projectID string, opts []option.ClientOption) error {
	client, err := run.NewServicesClient(ctx, opts...)
	if err != nil {
		return err
	}
	defer client.Close()
	it := client.ListServices(ctx, &runpb.ListServicesRequest{
		Parent:   fmt.Sprintf("projects/%s/locations/-", projectID),
		PageSize: 1,
	})
	_, err = it.Next()
	if err == iterator.Done {
		return nil
	}
	return err
}

// probeGKE issues a single ListClusters call across all locations.
func probeGKE(ctx context.Context, projectID string, opts []option.ClientOption) error {
	client, err := container.NewClusterManagerClient(ctx, opts...)
	if err != nil {
		return err
	}
	defer client.Close()
	_, err = client.ListClusters(ctx, &containerpb.ListClustersRequest{
		Parent: fmt.Sprintf("projects/%s/locations/-", projectID),
	})
	return err
}

// probeGCE issues a single aggregated instances list call.
func probeGCE(ctx context.Context, projectID string, opts []option.ClientOption) error {
	client, err := compute.NewInstancesRESTClient(ctx, opts...)
	if err != nil {
		return err
	}
	defer client.Close()
	maxResults := uint32(1)
	it := client.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{
		Project:    projectID,
		MaxResults: &maxResults,
	})
	_, err = it.Next()
	if err == iterator.Done {
		return nil
	}
	return err
}

// isGCPAuthErr reports whether a GCP error is an authorization/permission failure (as opposed to a
// connectivity/config error). It covers both gRPC (Cloud Run, GKE, Resource Manager) and REST (Compute)
// clients.
func isGCPAuthErr(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := status.FromError(err); ok {
		if st.Code() == codes.PermissionDenied || st.Code() == codes.Unauthenticated {
			return true
		}
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		if gerr.Code == 403 || gerr.Code == 401 {
			return true
		}
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "permissiondenied") || strings.Contains(s, "permission denied") ||
		strings.Contains(s, "forbidden") || strings.Contains(s, "not authorized")
}

// summarizeGCPErr returns a concise, secret-free description of a GCP error.
func summarizeGCPErr(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := status.FromError(err); ok {
		return fmt.Sprintf("%s: %s", st.Code(), st.Message())
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return fmt.Sprintf("%d: %s", gerr.Code, gerr.Message)
	}
	return err.Error()
}
