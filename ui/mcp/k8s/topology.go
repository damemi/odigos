package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/frontend/services"
)

// Topology = the service-call graph computed from OTLP service-graph metrics on
// the cluster collector. Like get_overview_metrics, this is live-only data.
func registerTopologyTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("get_service_map",
			readAnno(),
			mcp.WithDescription("Service-call topology: which service talks to which, with request counts. Includes virtual nodes for uninstrumented external services (DBs, third-party APIs). Use to answer 'what depends on this service' or 'what is hitting our database'."),
		),
		getServiceMapHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("get_peer_sources",
			readAnno(),
			mcp.WithDescription("Inbound and outbound peer services for one service. Smaller / focused alternative to get_service_map when you already know the service."),
			mcp.WithString("service_name", mcp.Required(), mcp.Description("Service name as it appears in the service map (matches OTel service.name).")),
			mcp.WithString("namespace", mcp.Description("Kubernetes namespace of the service, to tell apart same-named services in different namespaces.")),
		),
		getPeerSourcesHandler(deps),
	)
}

func getServiceMapHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.MetricsConsumer == nil {
			return mcp.NewToolResultError("metrics consumer not configured"), nil
		}
		edges := deps.MetricsConsumer.GetServiceGraphEdges()
		type toEdge struct {
			ToNodeID     string `json:"toNodeId"`
			RequestCount int64  `json:"requestCount"`
			Virtual      bool   `json:"toIsVirtual"`
		}
		type fromEntry struct {
			NodeID      string   `json:"nodeId"`
			ServiceName string   `json:"serviceName"`
			To          []toEdge `json:"to"`
		}
		out := make([]fromEntry, 0, len(edges))
		for fromKey, tos := range edges {
			fe := fromEntry{NodeID: fromKey, ServiceName: services.BaseServiceName(fromKey)}
			for toKey, e := range tos {
				fe.To = append(fe.To, toEdge{ToNodeID: toKey, RequestCount: e.RequestCount, Virtual: e.ToNodeIsVirtual})
			}
			out = append(out, fe)
		}
		return withGuidance(out, fmt.Sprintf("Topology has %d source nodes.", len(out)), []NextStep{
			{When: "to drill into one service's neighbours", Tool: "get_peer_sources"},
		})
	}
}

func getPeerSourcesHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.MetricsConsumer == nil {
			return mcp.NewToolResultError("metrics consumer not configured"), nil
		}
		svc, err := req.RequireString("service_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		peers := services.PeerSources(deps.MetricsConsumer.GetServiceGraphEdges(), svc, req.GetString("namespace", ""))
		return withGuidance(peers, fmt.Sprintf("Inbound and outbound peers for service %q.", svc), nil)
	}
}
