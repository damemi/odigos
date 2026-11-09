package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

func registerActionTools(s *server.MCPServer, deps Deps) {
	// Reads
	s.AddTool(
		mcp.NewTool("list_actions",
			readAnno(),
			mcp.WithDescription("List configured Actions (attribute transformers, PII masking, cluster info, URL templatization, …). Each row shows kind, signals, and disabled state. For sampling use the sampling tools — sampling is its own CRD, not an Action."),
		),
		listActionsHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_action",
			readAnno(),
			mcp.WithDescription("Return one Action with its full spec (whichever sub-config — piiMasking, deleteAttribute, renameAttribute, addClusterInfo, k8sAttributes, urlTemplatization, extractAttribute — is populated)."),
			mcp.WithString("action_name", mcp.Required(), mcp.Description("Action resource name from list_actions.")),
		),
		getActionHandler(),
	)

	// Writes — see actions_write.go
	s.AddTool(
		mcp.NewTool("create_action",
			writeAnno(),
			mcp.WithDescription("Create an Odigos Action (telemetry processing in the collector pipeline). Supported types: K8sAttributesResolver, AddClusterInfo, DeleteAttribute, RenameAttribute, PiiMasking, URLTemplatization, ExtractAttribute. Pass `type` and a `fields` object holding that type's config. NOTE: sampling is NOT an Action — it has its own Sampling CRD; use the sampling tools (create_sampling_group + the rule tools)."),
			mcp.WithString("type", mcp.Required(), mcp.Description("K8sAttributesResolver | AddClusterInfo | DeleteAttribute | RenameAttribute | PiiMasking | URLTemplatization | ExtractAttribute")),
			mcp.WithString("action_name", mcp.Description("Optional human-readable name.")),
			mcp.WithString("notes", mcp.Description("Optional free-form notes.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional: create the action disabled.")),
			mcp.WithArray("signals",
				mcp.Description("Signals to operate on. Defaults to [\"TRACES\"]."),
				mcp.Items(map[string]any{"type": "string", "enum": []string{"TRACES", "METRICS", "LOGS"}}),
			),
			mcp.WithObject("fields",
				mcp.Description("Type-specific config (the UI's ActionFieldsInput shape). Provide the fields for the chosen type: PiiMasking={piiCategories:[\"CREDIT_CARD\"]}; DeleteAttribute={attributeNamesToDelete:[…]}; RenameAttribute={renames:\"{\\\"old\\\":\\\"new\\\"}\"} (renames is a JSON-encoded string); AddClusterInfo={clusterAttributes:[{attributeName,attributeStringValue}],overwriteExistingValues}; K8sAttributesResolver={collectContainerAttributes,collectReplicaSetAttributes,collectWorkloadId,collectClusterId,labelsAttributes,annotationsAttributes}; URLTemplatization={urlTemplatizationRulesGroups:[…]}; ExtractAttribute={extractAttribute:{…}}."),
				mcp.AdditionalProperties(true),
			),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createActionHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_action",
			writeAnno(),
			mcp.WithDescription("Update an existing Action. Pass action_name plus only what you want to change: new_action_name, notes, disabled, signals[], and/or a `fields` object (same shape as create_action.fields). Sub-configs you don't send are preserved; the fields you send for a sub-config replace it. Omitted name/notes/disabled/signals keep their current values."),
			mcp.WithString("action_name", mcp.Required(), mcp.Description("Action resource name to update.")),
			mcp.WithString("new_action_name", mcp.Description("Optional: change the human-readable name.")),
			mcp.WithString("notes", mcp.Description("Optional: free-form notes.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional: enable/disable without deleting.")),
			mcp.WithArray("signals",
				mcp.Description("Optional: replace the signals list."),
				mcp.Items(map[string]any{"type": "string", "enum": []string{"TRACES", "METRICS", "LOGS"}}),
			),
			mcp.WithObject("fields", mcp.Description("Optional: type-specific config to replace (same shape as create_action.fields)."), mcp.AdditionalProperties(true)),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateActionHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_action",
			writeAnno(),
			mcp.WithDescription("Delete an Action by resource name."),
			mcp.WithString("action_name", mcp.Required(), mcp.Description("Action resource name from list_actions.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteActionHandler(),
	)
}

// ---- read handlers ---------------------------------------------------------

func listActionsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := env.GetCurrentNamespace()
		list, err := kube.DefaultClient.OdigosClient.Actions(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list actions: %v", err)), nil
		}
		type row struct {
			Name       string   `json:"name"`
			ActionName string   `json:"actionName"`
			Kinds      []string `json:"actionKinds"`
			Signals    []string `json:"signals"`
			Disabled   bool     `json:"disabled"`
		}
		out := make([]row, 0, len(list.Items))
		for _, a := range list.Items {
			signals := make([]string, 0, len(a.Spec.Signals))
			for _, sig := range a.Spec.Signals {
				signals = append(signals, string(sig))
			}
			out = append(out, row{Name: a.Name, ActionName: a.Spec.ActionName, Kinds: actionKinds(&a.Spec), Signals: signals, Disabled: a.Spec.Disabled})
		}
		return withGuidance(out, fmt.Sprintf("%d actions configured.", len(out)), []NextStep{
			{When: "to add a new sampler/transformer", Tool: "create_action"},
			{When: "to configure tail sampling rule groups instead (richer rules)", Tool: "get_sampling"},
		})
	}
}

func getActionHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("action_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ns := env.GetCurrentNamespace()
		var act odigosv1.Action
		if err := kube.CacheClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &act); err != nil {
			if apierrors.IsNotFound(err) {
				return mcp.NewToolResultError(fmt.Sprintf("action %q not found", name)), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("get action: %v", err)), nil
		}
		return withGuidance(act, "Full ActionSpec returned. Look at the populated sub-config (one of: piiMasking, samplers, addClusterInfo, deleteAttribute, renameAttribute, k8sAttributes, urlTemplatization, spanRenamer, extractAttribute) to see what this action does.", []NextStep{
			{When: "to change a field", Tool: "update_action", Args: map[string]any{"action_name": name}},
			{When: "to remove this action entirely", Tool: "delete_action", Args: map[string]any{"action_name": name}},
		})
	}
}

func deleteActionHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("action_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()
		if _, err := kube.DefaultClient.OdigosClient.Actions(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return withGuidance(map[string]any{"action": "noop"}, "Action not found — nothing to delete.", nil)
			}
			return mcp.NewToolResultError(fmt.Sprintf("get action: %v", err)), nil
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "delete_action", "action_name": name}, "Dry-run.", nil)
		}
		if err := kube.DefaultClient.OdigosClient.Actions(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete action: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "action_name": name}, "Deleted.", nil)
	}
}

// actionKinds returns a short list of populated sub-config kinds for the
// list view. Used by listActionsHandler.
func actionKinds(spec *odigosv1.ActionSpec) []string {
	kinds := []string{}
	if spec.AddClusterInfo != nil {
		kinds = append(kinds, "AddClusterInfo")
	}
	if spec.DeleteAttribute != nil {
		kinds = append(kinds, "DeleteAttribute")
	}
	if spec.RenameAttribute != nil {
		kinds = append(kinds, "RenameAttribute")
	}
	if spec.PiiMasking != nil {
		kinds = append(kinds, "PiiMasking")
	}
	if spec.K8sAttributes != nil {
		kinds = append(kinds, "K8sAttributes")
	}
	if spec.URLTemplatization != nil {
		kinds = append(kinds, "URLTemplatization")
	}
	if spec.SpanRenamer != nil {
		kinds = append(kinds, "SpanRenamer")
	}
	if spec.ExtractAttribute != nil {
		kinds = append(kinds, "ExtractAttribute")
	}
	return kinds
}
