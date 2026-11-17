package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/profiles"
)

// Presets are curated bundles of OdigosConfiguration tweaks (full-payload-collection,
// db-payload-collection, java-enterprise, ebpf-log-capture, mount-method-*, …).
// They're applied by adding the profile name to OdigosConfiguration.profiles[]
// in the odigos-configuration ConfigMap.
//
// list_profiles reuses the OSS profiles package; apply_profile edits the ConfigMap
// directly because OSS exposes no service that applies a profile.
func registerPresetTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("list_profiles",
			readAnno(),
			mcp.WithDescription("List curated profiles (presets). Each profile bundles a set of OdigosConfiguration tweaks (e.g. full-payload-collection, ebpf-log-capture)."),
		),
		listProfilesHandler(),
	)
	s.AddTool(
		mcp.NewTool("apply_profile",
			writeAnno(),
			mcp.WithDescription("Apply a curated profile by adding it to OdigosConfiguration.profiles[] in the odigos-configuration ConfigMap. WARNING: this triggers a control-plane reconcile and may roll out instrumented workloads."),
			mcp.WithString("profile_name", mcp.Required(), mcp.Description("Profile name from list_profiles.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		applyProfileHandler(),
	)
}

func listProfilesHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ps := profiles.AllProfiles
		type row struct {
			Name             string   `json:"name"`
			ShortDescription string   `json:"shortDescription"`
			Dependencies     []string `json:"dependencies,omitempty"`
		}
		out := make([]row, 0, len(ps))
		for _, p := range ps {
			deps := make([]string, 0, len(p.Dependencies))
			for _, d := range p.Dependencies {
				deps = append(deps, string(d))
			}
			out = append(out, row{Name: string(p.ProfileName), ShortDescription: p.ShortDescription, Dependencies: deps})
		}
		return withGuidance(out, fmt.Sprintf("%d profiles available.", len(out)), []NextStep{
			{When: "to apply one", Tool: "apply_profile"},
		})
	}
}

func applyProfileHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("profile_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()
		profileName := common.ProfileName(name)

		// The odigos-configuration ConfigMap is the hottest write target (every
		// config-touching tool RMWs it), so a 409 here is the most likely conflict
		// in the whole catalog. Re-fetch + re-apply rather than fail the call.
		var done *mcp.CallToolResult
		applyErr := retry.RetryOnConflict(conflictBackoff, func() error {
			cm, err := kube.DefaultClient.CoreV1().ConfigMaps(ns).Get(ctx, consts.OdigosConfigurationName, metav1.GetOptions{})
			if err != nil {
				done = mcp.NewToolResultError(fmt.Sprintf("get odigos-configuration: %v", err))
				return nil
			}
			raw, ok := cm.Data[consts.OdigosConfigurationFileName]
			if !ok {
				done = mcp.NewToolResultError(fmt.Sprintf("key %q missing from ConfigMap", consts.OdigosConfigurationFileName))
				return nil
			}
			var cfg common.OdigosConfiguration
			if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
				done = mcp.NewToolResultError(fmt.Sprintf("parse config: %v", err))
				return nil
			}

			for _, p := range cfg.Profiles {
				if p == profileName {
					done, _ = withGuidance(map[string]any{"action": "noop", "profile_name": name}, "Profile already applied.", nil)
					return nil
				}
			}
			if dryRun {
				done, _ = withGuidance(map[string]any{"action": "apply_profile", "profile_name": name, "would_append_to": "profiles[]"},
					"Dry-run: would add this profile to OdigosConfiguration.profiles[] and trigger reconcile.", []NextStep{
						{When: "to actually apply", Tool: "apply_profile", Args: map[string]any{"profile_name": name, "dry_run": false}},
					})
				return nil
			}

			cfg.Profiles = append(cfg.Profiles, profileName)
			buf, err := yaml.Marshal(&cfg)
			if err != nil {
				done = mcp.NewToolResultError(fmt.Sprintf("marshal config: %v", err))
				return nil
			}
			cm.Data[consts.OdigosConfigurationFileName] = string(buf)
			if _, err := kube.DefaultClient.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
				return err
			}
			done, _ = withGuidance(map[string]any{"action": "applied", "profile_name": name},
				"Profile applied. Control plane will reconcile and may roll out instrumented workloads.", []NextStep{
					{When: "to confirm via the effective config", Tool: "get_effective_config"},
				})
			return nil
		})
		if applyErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update configmap: %v", applyErr)), nil
		}
		return done, nil
	}
}
