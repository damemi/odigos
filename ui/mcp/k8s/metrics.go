package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Metrics tools surface the per-source/per-destination throughput the UI shows
// (computed from OTLP samples the metrics consumer ingests). These numbers are
// only reachable from the frontend process — they're not stored in any CRD.
func registerMetricsTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("get_overview_metrics",
			readAnno(),
			mcp.WithDescription("Per-source and per-destination cumulative bytes-sent and current throughput (bytes/sec, ~20s window). Primary answer to 'how much data is each workload sending' and 'how much is each destination receiving'. Live-computed; not stored in any CRD."),
		),
		getOverviewMetricsHandler(deps),
	)
}

func getOverviewMetricsHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.MetricsConsumer == nil {
			return mcp.NewToolResultError("metrics consumer not configured"), nil
		}
		type sourceRow struct {
			Namespace     string `json:"namespace"`
			Kind          string `json:"kind"`
			Name          string `json:"name"`
			TotalDataSent int64  `json:"totalDataSent"`
			Throughput    int64  `json:"throughputBytesPerSec"`
		}
		type destRow struct {
			ID            string `json:"id"`
			TotalDataSent int64  `json:"totalDataSent"`
			Throughput    int64  `json:"throughputBytesPerSec"`
		}
		srcMetrics := deps.MetricsConsumer.GetSourcesMetrics()
		destMetrics := deps.MetricsConsumer.GetDestinationsMetrics()
		srcs := make([]sourceRow, 0, len(srcMetrics))
		for sID, m := range srcMetrics {
			srcs = append(srcs, sourceRow{
				Namespace: sID.Namespace, Kind: string(sID.Kind), Name: sID.Name,
				TotalDataSent: m.TotalDataSent(), Throughput: m.TotalThroughput(),
			})
		}
		dests := make([]destRow, 0, len(destMetrics))
		for id, m := range destMetrics {
			dests = append(dests, destRow{ID: id, TotalDataSent: m.TotalDataSent(), Throughput: m.TotalThroughput()})
		}
		return withGuidance(map[string]any{"sources": srcs, "destinations": dests},
			fmt.Sprintf("%d sources and %d destinations reporting metrics.", len(srcs), len(dests)),
			[]NextStep{
				{When: "to see the call graph between services", Tool: "get_service_map"},
				{When: "if a destination shows zero throughput, to verify credentials", Tool: "test_destination_connection"},
				{When: "to drill into one workload's health", Tool: "describe_source"},
			})
	}
}
