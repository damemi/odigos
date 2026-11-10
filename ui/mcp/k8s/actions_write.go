package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/frontend/services"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// Supported action types are derived from the OSS GraphQL enum (model.AllActionType /
// ActionType.IsValid), NOT hardcoded here: the per-type field mapping lives in OSS
// (getSpecFromInput), so when OSS adds a new action type it becomes creatable through
// this tool automatically, with no change in this repo. UnknownType is the
// not-an-action sentinel and is excluded. Sampling is intentionally absent — it is
// its own Sampling CRD, exposed through the sampling tools, not an Action.
func isSupportedActionType(t string) bool {
	actionType := model.ActionType(t)
	return actionType.IsValid() && actionType != model.ActionTypeUnknownType
}

// supportedActionTypeNames is the list of accepted types, surfaced to the caller in
// the error when an unknown type is passed so it can self-correct.
func supportedActionTypeNames() []string {
	names := make([]string, 0, len(model.AllActionType))
	for _, actionType := range model.AllActionType {
		if actionType == model.ActionTypeUnknownType {
			continue
		}
		names = append(names, string(actionType))
	}
	return names
}

// createActionHandler creates an Action by delegating to the OSS action service
// (services.CreateAction) with the same ActionInput the GraphQL UI uses, so the
// MCP path and the UI path build the spec identically.
func createActionHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		actionType, err := req.RequireString("type")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !isSupportedActionType(actionType) {
			return errorWithOptions(fmt.Sprintf("unsupported action type %q", actionType), supportedActionTypeNames())
		}

		input := model.ActionInput{
			Type:     model.ActionType(actionType),
			Disabled: boolArg(req, "disabled", false),
			Signals:  signalsArg(req, []model.SignalType{model.SignalTypeTraces}),
		}
		if name := stringArg(req, "action_name", ""); name != "" {
			input.Name = &name
		}
		if notes := stringArg(req, "notes", ""); notes != "" {
			input.Notes = &notes
		}
		var fields model.ActionFieldsInput
		if _, err := decodeArg(req, "fields", &fields); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		input.Fields = &fields

		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "create_action", "type": actionType},
				"Dry-run: validated, nothing applied.", []NextStep{
					{When: "to actually apply", Tool: "create_action", Args: map[string]any{"type": actionType, "dry_run": false}},
				})
		}
		created, err := services.CreateAction(ctx, input)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create action: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "created", "id": created.ID},
			"Created. The controller compiles it into a collector Processor.", []NextStep{
				{When: "to confirm it appeared in the pipeline", Tool: "list_actions"},
			})
	}
}

// updateActionHandler updates an Action via services.UpdateAction. OSS preserves
// sub-configs the caller doesn't send, but overwrites the top-level
// name/notes/disabled/signals from the input — so we pre-fill those from the
// existing action and only override the ones the caller actually provided.
func updateActionHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("action_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ns := env.GetCurrentNamespace()
		existing, err := kube.DefaultClient.OdigosClient.Actions(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return mcp.NewToolResultError(fmt.Sprintf("action %q not found", name)), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("get action: %v", err)), nil
		}

		// Preserve the current top-level fields unless the caller overrides them.
		actionName := existing.Spec.ActionName
		if v := stringArg(req, "new_action_name", ""); v != "" {
			actionName = v
		}
		notes := existing.Spec.Notes
		if v := stringArg(req, "notes", ""); v != "" {
			notes = v
		}
		disabled := existing.Spec.Disabled
		if dp := boolArgPtr(req, "disabled"); dp != nil {
			disabled = *dp
		}

		input := model.ActionInput{
			Type:     model.ActionType(actionType(&existing.Spec)),
			Name:     &actionName,
			Notes:    &notes,
			Disabled: disabled,
			Signals:  signalsArg(req, signalsToModel(existing.Spec.Signals)),
		}
		var fields model.ActionFieldsInput
		if _, err := decodeArg(req, "fields", &fields); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		input.Fields = &fields

		if boolArg(req, "dry_run", true) {
			return withGuidance(map[string]any{"action": "update_action", "action_name": name}, "Dry-run.", []NextStep{
				{When: "to actually apply", Tool: "update_action", Args: map[string]any{"action_name": name, "dry_run": false}},
			})
		}
		updated, err := services.UpdateAction(ctx, name, input)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update action: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "updated", "id": updated.ID},
			"Action updated; the controller recompiles it into a Processor.", nil)
	}
}

// signalsArg reads the signals[] argument as []model.SignalType, returning def
// when the caller did not provide any.
func signalsArg(req mcp.CallToolRequest, def []model.SignalType) []model.SignalType {
	raw := stringSliceArg(req, "signals")
	if len(raw) == 0 {
		return def
	}
	out := make([]model.SignalType, 0, len(raw))
	for _, s := range raw {
		out = append(out, model.SignalType(s))
	}
	return out
}

// signalsToModel converts stored ObservabilitySignal values back to the GraphQL
// model.SignalType used by ActionInput.
func signalsToModel(sigs []common.ObservabilitySignal) []model.SignalType {
	out := make([]model.SignalType, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, model.SignalType(string(s)))
	}
	return out
}

// actionType reports the populated sub-config's type for an existing Action. It is
// only used to label the ActionInput on update (the OSS spec builder keys off the
// fields, not this value), but keeping it accurate makes the input self-describing.
func actionType(spec *odigosv1.ActionSpec) string {
	switch {
	case spec.K8sAttributes != nil:
		return string(model.ActionTypeK8sAttributesResolver)
	case spec.AddClusterInfo != nil:
		return string(model.ActionTypeAddClusterInfo)
	case spec.DeleteAttribute != nil:
		return string(model.ActionTypeDeleteAttribute)
	case spec.RenameAttribute != nil:
		return string(model.ActionTypeRenameAttribute)
	case spec.PiiMasking != nil:
		return string(model.ActionTypePiiMasking)
	case spec.URLTemplatization != nil:
		return string(model.ActionTypeURLTemplatization)
	case spec.ExtractAttribute != nil:
		return string(model.ActionTypeExtractAttribute)
	default:
		return string(model.ActionTypeUnknownType)
	}
}
