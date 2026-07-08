// Package reconciler implements COMPUTE-PLATFORM reconcilers for the AWS connector — converging the separate
// Odigos agent installed on a discovered compute platform (ECS-EC2 cluster agent today; EKS/EC2 are stubs). These
// run in the base's compute-platform reconcile loop and are NOT gated on a destination (the agent owns export).
package reconciler

import (
	"context"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/computeplatform/store"
)

var log = connlog.Subsystem("aws-computeplatform-reconciler")

// CentralParams are the values the deployed Odigos ecs-agent needs to connect back to Odigos Central,
// mirroring how the connector itself (and the VM agent) register over the reverse WebSocket at /ws/proxy
// (see connectors/base/central/client.go wsURL): a backend URL plus an identity. The connector passes
// these into the agent task so the agent surfaces under Central the same way.
type CentralParams struct {
	// BackendURL is the central-backend base URL (Config.CentralBackendURL).
	BackendURL string
	// ConnectorName is the owning connector identity; the agent's proxyID is derived per cluster from it.
	ConnectorName string
	// Version is the connector runtime version (reported at registration).
	Version string
	// SchemaVersion is the agents-api schema version the agent reports.
	SchemaVersion string
}

// ECSEC2AgentReconciler implements base.ComputePlatformReconciler for the ecs-cluster service: instead of
// instrumenting individual tasks, it converges a single global Odigos ecs-agent on each enabled
// EC2-backed ECS cluster (kind ecs-cluster) and assigns instrumentation to it. The agent owns OTel
// export, so this flow is NOT gated on an OTLP destination.
//
// NOTE: the actual ECS deploy (register a task definition + run the agent as a daemon Service / RunTask,
// with rollback) is intentionally STUBBED in this release — it logs the intended action and the exact
// central-connection parameters the agent will receive. The mechanism, agent image and IAM are finalized
// in a follow-up; the source-intent plumbing and central params are wired here.
type ECSEC2AgentReconciler struct {
	store   *store.Store
	cache   *awsclient.Cache
	central CentralParams
	image   string
}

// NewECSEC2Agent builds the ecs-cluster compute-platform reconciler over the AWS store, config cache, central
// connection params and the agent image (Config.ECSAgentImage).
func NewECSEC2Agent(st *store.Store, cache *awsclient.Cache, central CentralParams, image string) *ECSEC2AgentReconciler {
	return &ECSEC2AgentReconciler{store: st, cache: cache, central: central, image: image}
}

func (r *ECSEC2AgentReconciler) Reconcile(ctx context.Context) error {
	clusters, err := r.enabledClusterSources(ctx)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		// STUB: ensure the Odigos ecs-agent runs on this cluster, configured to reach Central. The real
		// ECS task/service creation is deferred; log the intended deploy + the agent's central params.
		log.Info("ecs-cluster service: would deploy odigos ecs-agent (stub)",
			"cluster", c.ServiceName,
			"region", c.Region,
			"clusterArn", c.ExternalResourceID,
			"agentImage", r.image,
			"centralEndpoint", r.central.BackendURL,
			"proxyID", r.proxyID(c),
			"platformType", "connector",
			"version", r.central.Version,
			"schemaVersion", r.central.SchemaVersion,
		)
	}
	// Rollback of agents on clusters whose source was removed is likewise stubbed until the deploy is
	// implemented (there is nothing live to tear down yet).
	return nil
}

// clusterSource is an enabled ecs-cluster source resolved to the cluster it targets.
type clusterSource struct {
	ServiceName        string // cluster name
	Region             string
	ExternalResourceID string // cluster ARN
}

// enabledClusterSources returns the enabled ecs-cluster source intents (mirrors the Lambda
// instrumentor's enabledLambdaSourceARNs, keyed on the cluster kind).
func (r *ECSEC2AgentReconciler) enabledClusterSources(ctx context.Context) ([]clusterSource, error) {
	sources, err := r.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	var out []clusterSource
	for _, s := range sources {
		if s.Kind == store.KindECSCluster && s.Enabled {
			out = append(out, clusterSource{
				ServiceName:        s.ServiceName,
				Region:             s.Region,
				ExternalResourceID: s.ExternalResourceID,
			})
		}
	}
	return out, nil
}

// proxyID derives the agent's stable Central connection id from the connector + cluster.
func (r *ECSEC2AgentReconciler) proxyID(c clusterSource) string {
	base := r.central.ConnectorName
	if base == "" {
		base = "connector"
	}
	return base + "-" + c.ServiceName
}
