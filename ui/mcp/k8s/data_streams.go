package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/frontend/services"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// Data streams are a logical grouping (e.g. "production", "staging") that bind
// a subset of Sources to a subset of Destinations. They're derived from labels
// on Source/Destination CRs (no dedicated CRD), so listing them means scanning
// both lists.
func registerDataStreamTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("list_data_streams",
			readAnno(),
			mcp.WithDescription("List unique data-stream names across all Sources and Destinations, with per-stream counts. Data streams are named groupings (e.g. 'default', 'staging') that map sources to destinations; they're a derived view."),
		),
		listDataStreamsHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_data_stream",
			writeAnno(),
			mcp.WithDescription("Rename a data stream. Cascades the rename across every Source and Destination labeled with the old name."),
			mcp.WithString("current_name", mcp.Required(), mcp.Description("Existing stream name (from list_data_streams).")),
			mcp.WithString("new_name", mcp.Required(), mcp.Description("New name to apply.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateDataStreamHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_data_stream",
			writeAnno(),
			mcp.WithDescription("Delete a data stream. Removes the stream label from every Source and Destination using it. WARNING: a Destination with no remaining streams may be deleted entirely (services.DeleteDestinationsOrRemoveStreamName)."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Stream name to delete.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteDataStreamHandler(),
	)
}

func updateDataStreamHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cur, err := req.RequireString("current_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		nw, err := req.RequireString("new_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		if cur == nw {
			return withGuidance(map[string]any{"action": "noop"}, "current_name == new_name; nothing to do.", nil)
		}
		ns := env.GetCurrentNamespace()
		if dryRun {
			return withGuidance(map[string]any{"action": "rename_data_stream", "current_name": cur, "new_name": nw},
				"Dry-run: would rename across every Source and Destination using this stream.", nil)
		}
		dests, err := kube.DefaultClient.OdigosClient.Destinations(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list destinations: %v", err)), nil
		}
		if err := services.UpdateDestinationsCurrentStreamName(ctx, dests, cur, nw); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update destinations: %v", err)), nil
		}
		srcs, err := kube.DefaultClient.OdigosClient.Sources("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sources: %v", err)), nil
		}
		if err := services.UpdateSourcesCurrentStreamName(ctx, srcs, cur, nw); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update sources: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "renamed", "current_name": cur, "new_name": nw}, "Stream renamed across Sources and Destinations.", nil)
	}
}

func deleteDataStreamHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()
		if dryRun {
			return withGuidance(map[string]any{"action": "delete_data_stream", "name": name},
				"Dry-run: would unlink the stream from every Source and Destination. Destinations with no remaining streams will be deleted.", nil)
		}
		dests, err := kube.DefaultClient.OdigosClient.Destinations(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list destinations: %v", err)), nil
		}
		if err := services.DeleteDestinationsOrRemoveStreamName(ctx, dests, name); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update destinations: %v", err)), nil
		}
		srcs, err := kube.DefaultClient.OdigosClient.Sources("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sources: %v", err)), nil
		}
		if err := services.DeleteSourcesOrRemoveStreamName(ctx, srcs, name); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update sources: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "name": name}, "Stream removed across all sources and destinations.", nil)
	}
}

func listDataStreamsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := env.GetCurrentNamespace()
		sources, err := kube.DefaultClient.OdigosClient.Sources("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sources: %v", err)), nil
		}
		dests, err := kube.DefaultClient.OdigosClient.Destinations(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list destinations: %v", err)), nil
		}

		counts := map[string]struct {
			Sources      int `json:"sourcesCount"`
			Destinations int `json:"destinationsCount"`
		}{}
		bump := func(name string, src, dest int) {
			v := counts[name]
			v.Sources += src
			v.Destinations += dest
			counts[name] = v
		}
		for _, s := range sources.Items {
			for _, ds := range services.ExtractDataStreamsFromSource(&s, nil) {
				if ds != nil {
					bump(*ds, 1, 0)
				}
			}
		}
		for _, d := range dests.Items {
			for _, ds := range services.ExtractDataStreamsFromDestination(d) {
				if ds != nil {
					bump(*ds, 0, 1)
				}
			}
		}

		type row struct {
			Name              string `json:"name"`
			SourcesCount      int    `json:"sourcesCount"`
			DestinationsCount int    `json:"destinationsCount"`
		}
		out := make([]row, 0, len(counts))
		for name, v := range counts {
			out = append(out, row{Name: name, SourcesCount: v.Sources, DestinationsCount: v.Destinations})
		}
		return withGuidance(out, fmt.Sprintf("%d data streams in use.", len(out)), []NextStep{
			{When: "to see destinations bound to a stream", Tool: "list_destinations"},
			{When: "to see sources bound to a stream", Tool: "list_sources"},
		})
	}
}
