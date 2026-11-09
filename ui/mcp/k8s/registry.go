// Package k8s holds the Kubernetes-only Odigos MCP tools — the full control-plane
// catalog (sources, destinations, actions, rules, sampling, diagnostics, control,
// collectors, config, presets, profiling, …). Every handler reuses the existing
// OSS frontend services and the shared kube client (kube.DefaultClient), so the
// tools observe exactly what the GraphQL UI does, with no re-implemented data
// access and no new contract types.
//
// These tools are k8s-welded (they call kube directly) and therefore live in the
// enterprise UI module, NOT in the portable mcp/ module the VM agent imports.
// The host (ui/mcp) builds one mcp-go server, registers the portable shared
// catalog, then calls RegisterAll here to add the k8s-only tools on top.
package k8s

import (
	"github.com/go-logr/logr"
	"github.com/mark3labs/mcp-go/server"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	collectormetrics "github.com/odigos-io/odigos/frontend/services/collector_metrics"
	fecommon "github.com/odigos-io/odigos/frontend/services/common"
)

// Deps is the dependency bundle every domain register-function receives. It
// mirrors the GraphQL Resolver fields so MCP tools reuse the exact services the
// UI does. The host populates it from frontend/server.Deps (Bootstrap output).
type Deps struct {
	Logger          logr.Logger
	MetricsConsumer *collectormetrics.OdigosMetricsConsumer
	PromAPI         v1.API
	K8sCacheClient  client.Client
	ProfileStore    fecommon.ProfileStoreRef
}

// RegisterAll mounts every k8s-only tool onto s. The host calls this AFTER
// mounting the portable shared catalog; the four shared read tools
// (list_sources/destinations/actions/rules) are registered by the shared
// catalog with the canonical CRD-spec shape and intentionally override the
// catalog's own list_* here (mcp-go AddTool is last-wins), so the cross-platform
// contract for those four stays identical to the VM agent.
func RegisterAll(s *server.MCPServer, deps Deps) {
	// Resources (CRD-shaped).
	registerSourceTools(s, deps)
	registerWorkloadTools(s, deps)
	registerDistroTools(s, deps)
	registerDestinationTools(s, deps)
	registerActionTools(s, deps)
	registerRuleTools(s, deps)
	registerCustomInstrumentationTools(s, deps)
	registerDataStreamTools(s, deps)

	// Cluster control + diagnostics.
	registerControlTools(s, deps)
	registerSamplingTools(s, deps)
	registerDiagnoseTools(s, deps)

	// Live introspection + topology.
	registerMetricsTools(s, deps)
	registerTopologyTools(s, deps)
	registerCollectorTools(s, deps)

	// Configuration + presets + raw K8s + profiling.
	registerConfigTools(s, deps)
	registerPresetTools(s, deps)
	registerProfilingTools(s, deps)
	registerManifestTools(s, deps)
}
