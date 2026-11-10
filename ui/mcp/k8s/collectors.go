package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/frontend/services/collectors"
)

// Collectors = the gateway Deployment (cluster-level) + odiglet DaemonSet
// (per-node). These tools mirror what the UI's "Collectors" page shows.
func registerCollectorTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("get_gateway_info",
			readAnno(),
			mcp.WithDescription("Cluster-gateway Deployment status: rollout state, replicas (desired/healthy/failed), resource requests/limits, image version, HPA config. Primary 'is the gateway healthy' answer."),
		),
		getGatewayInfoHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("get_odiglet_info",
			readAnno(),
			mcp.WithDescription("Odiglet DaemonSet status: rollout state, per-node counts (desired/current/updated/available)."),
		),
		getOdigletInfoHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("list_odiglet_pods",
			readAnno(),
			mcp.WithDescription("Per-node odiglet pods with live throughput metrics (accepted/refused/exported spans from Prometheus). Use to find one slow/dropping node when overall throughput looks off."),
		),
		listOdigletPodsHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("list_gateway_pods",
			readAnno(),
			mcp.WithDescription("List cluster-gateway collector pods (the deployment replicas). Use to see which gateway pod is unhealthy when get_gateway_info shows Degraded."),
		),
		listGatewayPodsHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_collector_pod",
			readAnno(),
			mcp.WithDescription("Full manifest + lifecycle state for a single collector pod (gateway or odiglet). Use to investigate why a specific collector pod is restarting."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Pod namespace (usually the Odigos namespace).")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Pod name (from list_gateway_pods or list_odiglet_pods).")),
		),
		getCollectorPodHandler(),
	)
}

func getGatewayInfoHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		info, err := collectors.GetGatewayDeploymentInfo(ctx, deps.PromAPI)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("gateway info: %v", err)), nil
		}
		return withGuidance(info, "Gateway is the cluster-level collector that receives from odiglets and exports to destinations.", []NextStep{
			{When: "if Failed/Degraded, to inspect the controller logs", Tool: "set_component_log_level",
				Args: map[string]any{"component": "collector", "level": "debug"}},
		})
	}
}

func getOdigletInfoHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		info, err := collectors.GetOdigletDaemonSetInfo(ctx, deps.PromAPI)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("odiglet info: %v", err)), nil
		}
		return withGuidance(info, "Odiglet runs on every node; it injects agents into pods and runs the local node collector.", []NextStep{
			{When: "to see per-node throughput", Tool: "list_odiglet_pods"},
		})
	}
}

func listOdigletPodsHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.PromAPI == nil {
			return mcp.NewToolResultError("Prometheus API not configured — odiglet pod metrics unavailable"), nil
		}
		pods, err := collectors.GetOdigletPodsWithNodeCollectorContainerMetrics(ctx, deps.PromAPI)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("odiglet pods: %v", err)), nil
		}
		return withGuidance(pods, fmt.Sprintf("%d odiglet pods reporting.", len(pods)),
			[]NextStep{
				{When: "to drill into a workload on a noisy node", Tool: "describe_source"},
				{When: "to see pod details of one odiglet", Tool: "get_collector_pod"},
			})
	}
}

func listGatewayPodsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		selector := fmt.Sprintf("%s=%s", k8sconsts.OdigosCollectorRoleLabel, string(k8sconsts.CollectorsRoleClusterGateway))
		pods, err := collectors.GetPodsBySelector(ctx, selector)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("gateway pods: %v", err)), nil
		}
		return withGuidance(pods, fmt.Sprintf("%d gateway pods.", len(pods)),
			[]NextStep{
				{When: "for a specific pod's lifecycle and resources", Tool: "get_collector_pod"},
			})
	}
}

func getCollectorPodHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		details, err := collectors.GetCollectorPodDetails(ctx, ns, name)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("collector pod: %v", err)), nil
		}
		return withGuidance(details, "Full pod manifest + container states. If the pod is CrashLoopBackOff, look at container statuses for the reason.", []NextStep{
			{When: "to restart the pod", Tool: "restart_pod", Args: map[string]any{"namespace": ns, "name": name}},
		})
	}
}
