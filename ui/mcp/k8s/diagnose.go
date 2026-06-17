package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/frontend/services"
	"github.com/odigos-io/odigos/k8sutils/pkg/describe"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// Diagnose tools are the "tell me what's wrong" surface. They go beyond
// describe_source (single workload) to give the agent a cluster-wide picture
// and the diagnostic bundle the support team typically asks for.
func registerDiagnoseTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("describe_odigos",
			readAnno(),
			mcp.WithDescription("Cluster-wide Odigos health: version, tier, gateway + odiglet deployment status (replicas, failed counts + reasons), pipeline summary (source/destination counts, isSettled, hasErrors). Primary 'is Odigos itself healthy' answer."),
		),
		describeOdigosHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("get_instrumentation_health",
			readAnno(),
			mcp.WithDescription("Per-pod SDK/eBPF instrumentation health in a namespace: for each instrumentation instance, components (tracer/sampler/exporter), healthy flag, reason/message. Use to diagnose 'agent is injected but no telemetry is flowing'."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace to inspect.")),
		),
		getInstrumentationHealthHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("get_source_conditions",
			readAnno(),
			mcp.WithDescription("Aggregated 'other' conditions across all Sources — quick way to see which workloads are stuck or erroring without iterating describe_source."),
		),
		getSourceConditionsHandler(),
	)
	s.AddTool(
		mcp.NewTool("collect_diagnose_bundle",
			writeAnno(),
			mcp.WithDescription("Collect the diagnostic bundle the support team typically asks for: logs + CRDs + (optional) profiles + (optional) metrics + (optional) source-workload state, tarballed. Use dry_run=true first to see the size before generating."),
			mcp.WithBoolean("include_profiles", mcp.Description("Include continuous-profiling data. Default true.")),
			mcp.WithBoolean("include_metrics", mcp.Description("Include collector metrics. Default true.")),
			mcp.WithBoolean("include_source_workloads", mcp.Description("Include per-source workload state. Default false.")),
			mcp.WithArray("source_workload_namespaces",
				mcp.Description("Only include source-workload state for these namespaces. Implies include_source_workloads=true."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			mcp.WithBoolean("dry_run", mcp.Description("Default true: returns size + file count estimate without writing the tarball.")),
		),
		collectDiagnoseBundleHandler(deps),
	)
}

func describeOdigosHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := env.GetCurrentNamespace()
		analyze, err := describe.DescribeOdigos(ctx, kube.DefaultClient, kube.DefaultClient.OdigosClient, ns)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("describe odigos: %v", err)), nil
		}
		return withGuidance(analyze, "If isSettled=false or hasErrors=true, check gateway/odiglet status and per-source describe_source for the offending workload.",
			[]NextStep{
				{When: "to inspect gateway", Tool: "get_gateway_info"},
				{When: "to inspect odiglet", Tool: "get_odiglet_info"},
				{When: "to see which sources have errors", Tool: "list_sources"},
				{When: "to grab the full support bundle", Tool: "collect_diagnose_bundle"},
			})
	}
}

func getInstrumentationHealthHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		list, err := kube.DefaultClient.OdigosClient.InstrumentationInstances(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list instrumentation instances: %v", err)), nil
		}
		type component struct {
			Name, Type string
			Healthy    *bool  `json:"healthy"`
			Reason     string `json:"reason,omitempty"`
			Message    string `json:"message,omitempty"`
		}
		type instance struct {
			Name       string      `json:"name"`
			Healthy    *bool       `json:"healthy"`
			Reason     string      `json:"reason,omitempty"`
			Message    string      `json:"message,omitempty"`
			Components []component `json:"components"`
		}
		out := make([]instance, 0, len(list.Items))
		var unhealthy int
		for _, ii := range list.Items {
			comps := make([]component, 0, len(ii.Status.Components))
			for _, c := range ii.Status.Components {
				comps = append(comps, component{Name: c.Name, Type: string(c.Type), Healthy: c.Healthy, Reason: c.Reason, Message: c.Message})
			}
			if ii.Status.Healthy != nil && !*ii.Status.Healthy {
				unhealthy++
			}
			out = append(out, instance{Name: ii.Name, Healthy: ii.Status.Healthy, Reason: ii.Status.Reason, Message: ii.Status.Message, Components: comps})
		}
		ctxNote := fmt.Sprintf("%d instances; %d unhealthy. Look at each instance's components — an unhealthy 'exporter' usually means a destination issue; an unhealthy 'instrumentation' usually means runtime/agent issues.", len(out), unhealthy)
		return withGuidance(map[string]any{"namespace": ns, "instances": out}, ctxNote, []NextStep{
			{When: "if an exporter is unhealthy", Tool: "test_destination_connection"},
			{When: "if instrumentation is unhealthy on a workload", Tool: "describe_source"},
		})
	}
}

func getSourceConditionsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		out, err := services.GetOtherConditionsForSources(ctx, "", "", "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("source conditions: %v", err)), nil
		}
		return withGuidance(out, fmt.Sprintf("%d sources reporting conditions.", len(out)),
			[]NextStep{
				{When: "to drill into one source with non-success conditions", Tool: "describe_source"},
			})
	}
}

func collectDiagnoseBundleHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dryRun := boolArg(req, "dry_run", true)
		incProfiles := boolArgPtr(req, "include_profiles")
		incMetrics := boolArgPtr(req, "include_metrics")
		incSrc := boolArgPtr(req, "include_source_workloads")
		nss := stringSliceArg(req, "source_workload_namespaces")

		input := &model.DiagnoseInput{
			IncludeProfiles:          incProfiles,
			IncludeMetrics:           incMetrics,
			IncludeSourceWorkloads:   incSrc,
			SourceWorkloadNamespaces: nss,
		}
		resp, err := services.DiagnoseGraphQL(ctx, input, &dryRun)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("diagnose: %v", err)), nil
		}
		return withGuidance(resp, "If dry_run=true, the response includes a size estimate. Re-call with dry_run=false to actually write the tarball.",
			[]NextStep{
				{When: "to actually generate the tarball after reviewing the size", Tool: "collect_diagnose_bundle",
					Args: map[string]any{"dry_run": false}},
			})
	}
}
