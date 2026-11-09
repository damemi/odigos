package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/services"
)

// Cluster-control tools: heavy, scope-wide operations the agent should only
// reach for after diagnosing a specific failure. Each tool's description names
// the failure mode it's for, so the agent doesn't pause Odigos when a simple
// restart_pod would do.
func registerControlTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("restart_pod",
			writeAnno(),
			mcp.WithDescription("Delete a pod so its controller (Deployment/etc.) recreates it. Use for: a single pod stuck in CrashLoopBackOff, a one-off agent reattach. For all pods behind a workload, use restart_workloads."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Pod namespace.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Pod name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		restartPodHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("restart_workloads",
			writeAnno(),
			mcp.WithDescription("Rolling-restart one or more workloads. Use after changing instrumentation config that requires a pod rollout (community distros), or to re-inject the agent."),
			mcp.WithArray("sources", mcp.Required(),
				mcp.Description("Array of {namespace, kind, name} workload identifiers."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		restartWorkloadsHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("recover_from_rollback",
			writeAnno(),
			mcp.WithDescription("After Odigos auto-rolled-back instrumentation on a workload (because the agent caused a crash), this lifts the rollback protection so the workload can be re-instrumented. Call this only after the root cause is fixed (e.g. you switched to a supported distro)."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		recoverFromRollbackHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("pause_odigos",
			writeAnno(),
			mcp.WithDescription("Pause all Odigos reconciliation cluster-wide by scaling the instrumentor + odiglet to zero. WARNING: agent injection stops; existing pods keep their agents but new pods come up uninstrumented. Use only as a panic button."),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		pauseOdigosHandler(),
	)
	s.AddTool(
		mcp.NewTool("uninstrument_cluster",
			writeAnno(),
			mcp.WithDescription("Remove all instrumentation cluster-wide by deleting every Source and disabling per-namespace instrumentation. WARNING: telemetry stops for every workload; pods restart. Use only when actually removing Odigos."),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		uninstrumentClusterHandler(),
	)
}

func restartPodHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "restart_pod", "namespace": ns, "name": name}, "Dry-run.", []NextStep{
				{When: "to actually apply", Tool: "restart_pod", Args: map[string]any{"namespace": ns, "name": name, "dry_run": false}},
			})
		}
		if err := services.RestartPod(ctx, ns, name); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("restart pod: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "restarted", "namespace": ns, "name": name}, "Pod deleted; its controller will recreate it.", nil)
	}
}

func restartWorkloadsHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var ids []struct {
			Namespace string `json:"namespace"`
			Kind      string `json:"kind"`
			Name      string `json:"name"`
		}
		ok, err := decodeArg(req, "sources", &ids)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !ok || len(ids) == 0 {
			return mcp.NewToolResultError("sources must be a non-empty array of {namespace, kind, name}"), nil
		}
		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "restart_workloads", "count": len(ids), "ids": ids}, "Dry-run.", nil)
		}

		// Trigger a kubectl-rollout-restart-style patch on each workload.
		errs := []map[string]any{}
		successCount := 0
		for _, id := range ids {
			k := model.K8sResourceKind(strings.Title(strings.ToLower(id.Kind)))
			if err := services.RolloutRestartWorkload(ctx, id.Namespace, id.Name, k); err != nil {
				errs = append(errs, map[string]any{"ns": id.Namespace, "kind": id.Kind, "name": id.Name, "err": err.Error()})
				continue
			}
			successCount++
		}
		result := map[string]any{
			"action":       "restarted",
			"successCount": successCount,
			"failures":     errs,
		}
		ctxNote := fmt.Sprintf("Restarted %d of %d workloads.", successCount, len(ids))
		return withGuidance(result, ctxNote, []NextStep{
			{When: "to confirm new pods came up healthy", Tool: "get_instrumentation_health"},
		})
	}
}

func recoverFromRollbackHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kindStr, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "recover_from_rollback", "namespace": ns, "kind": kindStr, "name": name}, "Dry-run.", []NextStep{
				{When: "to actually apply", Tool: "recover_from_rollback", Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name, "dry_run": false}},
			})
		}
		if err := services.RecoverFromRollback(ctx, deps.K8sCacheClient, ns, name, kindStr); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("recover_from_rollback: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "recovered", "namespace": ns, "kind": kindStr, "name": name},
			"Rollback protection lifted. The next reconcile will attempt instrumentation again.", []NextStep{
				{When: "to watch the next attempt", Tool: "describe_source", Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
			})
	}
}

func pauseOdigosHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "pause_odigos"}, "Dry-run. This is a panic button — confirm you really want to pause cluster-wide.", nil)
		}
		if err := services.PauseOdigos(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("pause odigos: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "paused"}, "Odigos reconciliation paused. Existing pod agents remain; new pods come up uninstrumented.", nil)
	}
}

func uninstrumentClusterHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "uninstrument_cluster"}, "Dry-run. This wipes ALL Sources — pods restart, telemetry stops.", nil)
		}
		if err := services.UninstrumentCluster(ctx); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("uninstrument cluster: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "uninstrumented"}, "Every Source removed; cluster is uninstrumented.", nil)
	}
}
