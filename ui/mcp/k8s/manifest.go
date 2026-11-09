package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/services"
)

// manifest_tools expose generic K8s resource introspection — the agent can
// fetch raw YAML for any of the kinds the Odigos UI supports.
func registerManifestTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("get_k8s_manifest",
			readAnno(),
			mcp.WithDescription("Return the raw YAML of any K8s resource Odigos knows about (Deployment, DaemonSet, StatefulSet, CronJob, ConfigMap, Pod, StaticPod, DeploymentConfig, Rollout, InstrumentationConfig). ManagedFields are stripped. Use sparingly — these YAMLs are large."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace of the resource.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("K8s resource kind (Deployment/DaemonSet/StatefulSet/CronJob/ConfigMap/Pod/StaticPod/DeploymentConfig/Rollout/InstrumentationConfig).")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Resource name.")),
		),
		getK8sManifestHandler(),
	)
}

func getK8sManifestHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		kindStr, err := req.RequireString("kind")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		yaml, err := services.K8sManifest(ctx, ns, model.K8sResourceKind(kindStr), name)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("k8s manifest: %v", err)), nil
		}
		// Return raw YAML text directly — it's already a string and adding
		// withGuidance JSON-wrapping it would just escape it.
		return mcp.NewToolResultText(yaml), nil
	}
}
