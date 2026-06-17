package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/frontend/services/profiles"
	"github.com/odigos-io/odigos/frontend/services/profiles/flamegraph"
)

func registerProfilingTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("list_profiling_slots",
			readAnno(),
			mcp.WithDescription("List active continuous-profiling slots and store memory stats. Use to see which sources are currently being profiled and whether profile data has been collected."),
		),
		listProfilingSlotsHandler(deps),
	)

	s.AddTool(
		mcp.NewTool("enable_source_profiling",
			writeAnno(),
			mcp.WithDescription("Open an on-demand profiling slot for a workload so samples accumulate in the store. Call this before reading a profile."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind: Deployment, DaemonSet, or StatefulSet.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		),
		enableSourceProfilingHandler(deps),
	)

	s.AddTool(
		mcp.NewTool("disable_source_profiling",
			writeAnno(),
			mcp.WithDescription("Close a workload's profiling slot and free its buffered profile data."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		),
		disableSourceProfilingHandler(deps),
	)

	s.AddTool(
		mcp.NewTool("clear_source_profiling_buffer",
			writeAnno(),
			mcp.WithDescription("Drop a workload's buffered profile samples but keep the slot active. Useful before reproducing an issue so the resulting profile is unmuddied."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		),
		clearSourceProfilingBufferHandler(deps),
	)

	s.AddTool(
		mcp.NewTool("get_source_profile",
			readAnno(),
			mcp.WithDescription("Return the full Pyroscope flamebearer (names + levels + symbol top-table) for a workload. WARNING: large response. For picking a function to instrument, prefer get_source_hot_functions."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		),
		getSourceProfileHandler(deps),
	)

	s.AddTool(
		mcp.NewTool("get_source_hot_functions",
			readAnno(),
			mcp.WithDescription("Top hot functions for a workload ranked by self time (excludes callees). This is the function to reason over when deciding where to add a custom probe: pick a hot function you own, then call add_custom_instrumentation. Context-window friendly — only the top N rows."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
			mcp.WithNumber("top_n", mcp.Description("How many hot functions to return. Default 15.")),
		),
		getSourceHotFunctionsHandler(deps),
	)
}

func listProfilingSlotsHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		store := deps.ProfileStore
		if store == nil {
			return withGuidance(map[string]any{"active": false}, "Profiling store is not configured on this frontend instance.", nil)
		}
		activeKeys, keysWithData := store.ActiveSlots()
		stats := store.MemoryStats()
		data := map[string]any{
			"activeKeys":          activeKeys,
			"keysWithData":        keysWithData,
			"maxSlots":            stats.MaxSlots,
			"totalBytesUsed":      stats.TotalBytes,
			"slotMaxBytes":        stats.SlotMaxBytes,
			"slotTTLSeconds":      stats.SlotTTLSeconds,
			"maxTotalBytesBudget": stats.MaxTotalBytesBudget,
		}
		ctxNote := fmt.Sprintf("%d active slots; %d have collected data.", len(activeKeys), len(keysWithData))
		steps := []NextStep{
			{When: "to start profiling a workload", Tool: "enable_source_profiling"},
			{When: "to read a workload's hot functions", Tool: "get_source_hot_functions"},
		}
		return withGuidance(data, ctxNote, steps)
	}
}

func enableSourceProfilingHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kind, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := profiles.EnableProfilingForSource(deps.ProfileStore, ns, kind, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		steps := []NextStep{
			{When: "once samples have accumulated (typically 30s+)", Tool: "get_source_hot_functions",
				Args: map[string]any{"namespace": ns, "kind": kind, "name": name}},
		}
		return withGuidance(out, "Profiling slot is open; samples will accumulate as the workload runs.", steps)
	}
}

func disableSourceProfilingHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kind, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := profiles.DisableProfilingForSource(deps.ProfileStore, ns, kind, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return withGuidance(out, "Profiling slot closed and buffer freed.", nil)
	}
}

func clearSourceProfilingBufferHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kind, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := profiles.ClearProfilingBufferForSource(deps.ProfileStore, ns, kind, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		steps := []NextStep{
			{When: "after reproducing the issue, to view the resulting profile", Tool: "get_source_hot_functions",
				Args: map[string]any{"namespace": ns, "kind": kind, "name": name}},
		}
		return withGuidance(out, "Buffer cleared; the slot is still active and new samples will accumulate.", steps)
	}
}

func getSourceProfileHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kind, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := profiles.GetProfilingForSource(ctx, deps.ProfileStore, ns, kind, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Profile is large — return raw payload, no wrapping.
		b, err := json.Marshal(out.Profile)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("marshal profile: %v", err)), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

func getSourceHotFunctionsHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kind, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		topN := intArg(req, "top_n", 15)
		if topN <= 0 {
			topN = 15
		}
		out, err := profiles.GetProfilingForSource(ctx, deps.ProfileStore, ns, kind, name)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		rows, totalSelf := rankHotFunctions(out.Profile.Symbols, topN)
		if len(rows) == 0 {
			steps := []NextStep{
				{When: "to start collecting profile samples for this workload", Tool: "enable_source_profiling",
					Args: map[string]any{"namespace": ns, "kind": kind, "name": name}},
			}
			return withGuidance(map[string]any{
				"namespace": ns, "kind": kind, "name": name, "hot_functions": []any{},
			}, "No profile data yet — enable profiling and let samples accumulate, then retry.", steps)
		}

		data := map[string]any{
			"namespace":          ns,
			"kind":               kind,
			"name":               name,
			"total_self_samples": totalSelf,
			"hot_functions":      rows,
		}

		// Suggest the agent's next move with concrete pre-filled args for each
		// language; the agent can pick whichever matches the workload.
		topFn := rows[0].Function
		steps := []NextStep{
			{When: "to add a Go span on the top hot function", Tool: "add_custom_instrumentation",
				Args: map[string]any{"language": "go", "package_name": "<pkg>", "function_name": topFn}},
			{When: "to add a Java span on the top hot function", Tool: "add_custom_instrumentation",
				Args: map[string]any{"language": "java", "class_name": "<fqcn>", "method_name": topFn}},
			{When: "to clear the buffer before reproducing the issue", Tool: "clear_source_profiling_buffer",
				Args: map[string]any{"namespace": ns, "kind": kind, "name": name}},
		}
		ctxNote := fmt.Sprintf("%q accounts for %.1f%% of self-time. Pick a hot function you own, then add_custom_instrumentation to get a span on it.",
			rows[0].Function, rows[0].SelfPercent)
		return withGuidance(data, ctxNote, steps)
	}
}

// HotFunction is one ranked row of the profile top-table.
type HotFunction struct {
	Function     string  `json:"function"`
	SelfSamples  int64   `json:"selfSamples"`
	SelfPercent  float64 `json:"selfPercent"`
	TotalSamples int64   `json:"totalSamples"`
}

// rankHotFunctions sorts the symbol top-table by self time (time in the
// function itself, excluding callees) and returns the top N with each
// function's share of total self time. Pure function so it can be unit-tested
// without a populated ProfileStore.
func rankHotFunctions(symbols []flamegraph.SymbolStats, topN int) ([]HotFunction, int64) {
	if len(symbols) == 0 {
		return nil, 0
	}
	if topN <= 0 {
		topN = 15
	}
	sorted := make([]flamegraph.SymbolStats, len(symbols))
	copy(sorted, symbols)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Self > sorted[j].Self })

	var totalSelf int64
	for _, s := range sorted {
		totalSelf += s.Self
	}
	if topN > len(sorted) {
		topN = len(sorted)
	}
	rows := make([]HotFunction, 0, topN)
	for _, s := range sorted[:topN] {
		pct := 0.0
		if totalSelf > 0 {
			pct = float64(s.Self) / float64(totalSelf) * 100
		}
		rows = append(rows, HotFunction{
			Function:     s.Name,
			SelfSamples:  s.Self,
			SelfPercent:  pct,
			TotalSamples: s.Total,
		})
	}
	return rows, totalSelf
}
