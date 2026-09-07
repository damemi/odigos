package k8s

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/services"
)

// Config tools surface what the UI's Config page knows: the merged effective
// config (helm + remote + local overrides) and the form schema for every
// configurable field. Write paths for individual settings (ignored namespaces,
// component log level, ui mode, etc.) are exposed through apply_profile and
// kube-level patches today; first-class config-write tools are a follow-up.
func registerConfigTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("get_effective_config",
			readAnno(),
			mcp.WithDescription("Return the merged effective OdigosConfiguration (helm + remote + local UI overrides) and its raw YAML. Large response. Use to answer 'what is the current cluster config' before any update."),
		),
		getEffectiveConfigHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("get_config_yamls",
			readAnno(),
			mcp.WithDescription("Return the UI form schema for every configurable field in OdigosConfiguration (field name + type + helm path + docs link + render conditions). This tells the agent what knobs exist before it tries to change anything."),
		),
		getConfigYamlsHandler(),
	)
	s.AddTool(
		mcp.NewTool("set_component_log_level",
			writeAnno(),
			mcp.WithDescription("Set log level for one Odigos component. Component: autoscaler | scheduler | instrumentor | odiglet | deviceplugin | ui | collector. Use to enable debug logging when troubleshooting."),
			mcp.WithString("component", mcp.Required(), mcp.Description("autoscaler | scheduler | instrumentor | odiglet | deviceplugin | ui | collector")),
			mcp.WithString("level", mcp.Required(), mcp.Description("debug | info | warn | error")),
		),
		setComponentLogLevelHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("update_remote_config",
			writeAnno(),
			mcp.WithDescription("Write the central-managed RemoteConfig. Today: rollout.automatic_rollout_disabled. (More fields wired through as central-backend exposes them.)"),
			mcp.WithBoolean("rollout_automatic_disabled", mcp.Description("If true, disables automatic rollouts for instrumentation changes.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateRemoteConfigHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("update_local_ui_config",
			writeAnno(),
			mcp.WithDescription("Write the local UI override of OdigosConfiguration. Use to set ignored namespaces/containers, telemetry-enabled, cluster name, GoAutoOffsets cron/mode, component log levels, sampling toggles, rollout / auto-rollback / WASP / instrumentor knobs. Pass only the fields you want to change."),
			mcp.WithBoolean("telemetry_enabled", mcp.Description("Optional.")),
			mcp.WithArray("ignored_namespaces", mcp.Description("Optional: replace ignored namespaces list."), mcp.Items(map[string]any{"type": "string"})),
			mcp.WithArray("ignored_containers", mcp.Description("Optional: replace ignored containers list."), mcp.Items(map[string]any{"type": "string"})),
			mcp.WithBoolean("ignore_odigos_namespace", mcp.Description("Optional.")),
			mcp.WithString("cluster_name", mcp.Description("Optional: name shown in UI / resource attributes.")),
			mcp.WithString("go_auto_offsets_cron", mcp.Description("Optional: cron schedule for Go agent auto-offsets refresh.")),
			mcp.WithString("go_auto_offsets_mode", mcp.Description("Optional: 'manual' | 'auto' | other modes per docs.")),
			mcp.WithObject("rollout", mcp.Description("Optional: rollout knobs (e.g. automaticRolloutDisabled, maxConcurrentRollouts)."), mcp.AdditionalProperties(true)),
			mcp.WithObject("auto_rollback", mcp.Description("Optional: autoRollback knobs (rollbackDisabled, rollbackGraceTime, rollbackStabilityWindow)."), mcp.AdditionalProperties(true)),
			mcp.WithObject("sampling", mcp.Description("Optional: global sampling toggles (dry_run, k8sHealthProbesSampling, etc)."), mcp.AdditionalProperties(true)),
			mcp.WithObject("wasp", mcp.Description("Optional: WASP knobs."), mcp.AdditionalProperties(true)),
			mcp.WithObject("instrumentor", mcp.Description("Optional: instrumentor knobs (agentEnvVarsInjectionMethod, checkDeviceHealthBeforeInjection)."), mcp.AdditionalProperties(true)),
			mcp.WithObject("allow_concurrent_agents", mcp.Description("Optional: allowConcurrentAgents knobs."), mcp.AdditionalProperties(true)),
			mcp.WithObject("component_log_levels", mcp.Description("Optional: per-component log levels (autoscaler, scheduler, instrumentor, odiglet, deviceplugin, ui, collector)."), mcp.AdditionalProperties(true)),
		),
		updateLocalUIConfigHandler(deps),
	)
	s.AddTool(
		mcp.NewTool("reset_local_ui_config",
			writeAnno(),
			mcp.WithDescription("Reset the local UI config to factory defaults (clears every override set by update_local_ui_config)."),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		resetLocalUIConfigHandler(deps),
	)
}

func getEffectiveConfigHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.K8sCacheClient == nil {
			return mcp.NewToolResultError("k8s cache client not configured"), nil
		}
		cfg, raw, err := services.GetEffectiveConfigWithRawYAML(ctx, deps.K8sCacheClient)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("effective config: %v", err)), nil
		}
		return withGuidance(map[string]any{"parsed": cfg, "rawYaml": raw},
			"This is what the controllers actually act on. To change a field, either apply a profile (apply_profile) or patch the helm/remote source.",
			[]NextStep{
				{When: "to see what fields exist + their docs", Tool: "get_config_yamls"},
				{When: "to apply a curated bundle of tweaks", Tool: "apply_profile"},
			})
	}
}

func getConfigYamlsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		yamls, err := services.GetConfigYamls()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("config yamls: %v", err)), nil
		}
		return withGuidance(yamls, fmt.Sprintf("%d config field groups.", len(yamls)), nil)
	}
}

func setComponentLogLevelHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		component, err := req.RequireString("component")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		level, err := req.RequireString("level")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		switch strings.ToLower(component) {
		case "autoscaler", "scheduler", "instrumentor", "odiglet", "deviceplugin", "ui", "collector":
		default:
			return errorWithOptions(fmt.Sprintf("unknown component %q", component),
				[]string{"autoscaler", "scheduler", "instrumentor", "odiglet", "deviceplugin", "ui", "collector"})
		}
		switch strings.ToLower(level) {
		case "debug", "info", "warn", "error":
		default:
			return errorWithOptions(fmt.Sprintf("unknown level %q", level), []string{"debug", "info", "warn", "error"})
		}
		if deps.K8sCacheClient == nil {
			return mcp.NewToolResultError("k8s cache client not configured"), nil
		}
		if err := services.SetComponentLogLevel(ctx, deps.K8sCacheClient, strings.ToLower(component), common.OdigosLogLevel(strings.ToLower(level))); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("set log level: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "set_log_level", "component": component, "level": level},
			"Component restarts pick up the new level on next reconcile.", nil)
	}
}

func updateRemoteConfigHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dryRun := boolArg(req, "dry_run", true)
		changed := map[string]any{}
		rolloutDisabled := boolArgPtr(req, "rollout_automatic_disabled")
		if rolloutDisabled != nil {
			changed["rollout.automaticRolloutDisabled"] = *rolloutDisabled
		}
		if len(changed) == 0 {
			return withGuidance(map[string]any{"action": "noop"}, "Nothing to change; supply at least one supported field.", nil)
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "update_remote_config", "would_change": changed}, "Dry-run.", nil)
		}
		if err := services.UpdateRemoteConfig(ctx, func(cfg *common.OdigosConfiguration) {
			if rolloutDisabled != nil {
				if cfg.Rollout == nil {
					cfg.Rollout = &common.RolloutConfiguration{}
				}
				cfg.Rollout.AutomaticRolloutDisabled = rolloutDisabled
			}
		}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update remote config: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "updated", "changed": changed}, "Remote config applied.", nil)
	}
}

func updateLocalUIConfigHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.K8sCacheClient == nil {
			return mcp.NewToolResultError("k8s cache client not configured"), nil
		}
		input := model.LocalUIConfigInput{}

		if dp := boolArgPtr(req, "telemetry_enabled"); dp != nil {
			input.TelemetryEnabled = dp
		}
		if v := stringSliceArg(req, "ignored_namespaces"); v != nil {
			input.IgnoredNamespaces = v
		}
		if v := stringSliceArg(req, "ignored_containers"); v != nil {
			input.IgnoredContainers = v
		}
		if dp := boolArgPtr(req, "ignore_odigos_namespace"); dp != nil {
			input.IgnoreOdigosNamespace = dp
		}
		if v := stringArg(req, "cluster_name", ""); v != "" {
			input.ClusterName = &v
		}
		if v := stringArg(req, "go_auto_offsets_cron", ""); v != "" {
			input.GoAutoOffsetsCron = &v
		}
		if v := stringArg(req, "go_auto_offsets_mode", ""); v != "" {
			input.GoAutoOffsetsMode = &v
		}
		if _, err := decodeArg(req, "rollout", &input.Rollout); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := decodeArg(req, "auto_rollback", &input.AutoRollback); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := decodeArg(req, "sampling", &input.Sampling); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := decodeArg(req, "wasp", &input.Wasp); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := decodeArg(req, "instrumentor", &input.Instrumentor); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := decodeArg(req, "allow_concurrent_agents", &input.AllowConcurrentAgents); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, err := decodeArg(req, "component_log_levels", &input.ComponentLogLevels); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if err := services.UpdateLocalUIConfig(ctx, deps.K8sCacheClient, input); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update local UI config: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "updated"}, "Local UI config overlay updated; components reconcile shortly.", nil)
	}
}

func resetLocalUIConfigHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if deps.K8sCacheClient == nil {
			return mcp.NewToolResultError("k8s cache client not configured"), nil
		}
		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "reset_local_ui_config"}, "Dry-run: would clear every local override.", nil)
		}
		if err := services.ResetLocalUiConfigToFactoryDefaults(ctx, deps.K8sCacheClient); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reset local UI: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "reset"}, "Local UI config cleared.", nil)
	}
}
