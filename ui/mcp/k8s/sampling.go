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

// Sampling tools cover collector-side tail-sampling. The Sampling CRD models
// three rule families:
//
//   - noisy operations (drop)         → sampling_noisy.go
//   - highly-relevant ops (always keep) → sampling_highly_relevant.go
//   - cost-reduction rules            → sampling_cost_reduction.go
//
// Each Sampling CR is a named "rule group". This file owns the group lifecycle
// (list / get / create / delete) and the tool registration; per-rule CRUD
// lives in the family-specific files, and the shared decoder + read-modify-
// write helpers live in sampling_matchers.go.
//
// These handlers write the Sampling CRD directly via the shared kube client:
// OSS exposes no frontend service for the Sampling CRD (unlike actions/rules,
// which delegate to services.*), so there is nothing to delegate to here.
//
// Identification used by all tools:
//
//	sampling_id = Sampling CR resource name
//	rule_name   = the Name field on a rule inside the group
//
// For AGENT-side head sampling, use create_action with
// action_type=probabilistic_sampler, or set head_sampling_fraction on
// create_instrumentation_rule.

func registerSamplingTools(s *server.MCPServer, deps Deps) {
	// Groups
	s.AddTool(
		mcp.NewTool("get_sampling",
			readAnno(),
			mcp.WithDescription("List Sampling CRs (rule groups) with per-group counts of noisy / highly-relevant / cost-reduction rules. Each Sampling CR is one group."),
		),
		listSamplingGroupsHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_sampling_group",
			readAnno(),
			mcp.WithDescription("Return one Sampling group's full spec (every rule of every kind)."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Sampling CR resource name (from get_sampling).")),
		),
		getSamplingGroupHandler(),
	)
	s.AddTool(
		mcp.NewTool("create_sampling_group",
			writeAnno(),
			mcp.WithDescription("STEP 1 of tail sampling: create an empty named Sampling group (a rule container). It samples nothing until you add rules with create_noisy/highly_relevant/cost_reduction_operation_rule. If a group already exists (see get_sampling), reuse its sampling_id instead of making another."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Human-readable group name.")),
			mcp.WithString("notes", mcp.Description("Optional free-form notes.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createSamplingGroupHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_sampling_group",
			writeAnno(),
			mcp.WithDescription("Delete a Sampling group and all its rules."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Sampling CR resource name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteSamplingGroupHandler(),
	)

	// Noisy-operation rules — sampling_noisy.go
	s.AddTool(
		mcp.NewTool("create_noisy_operation_rule",
			writeAnno(),
			mcp.WithDescription("ADD a NEW noisy-operation rule to an existing sampling group: DROP high-volume low-value traces (health checks, readiness probes, metrics scrapes), keeping AT MOST percentage_at_most%. Requires a group — call create_sampling_group first if none exists. To change an existing rule use update_noisy_operation_rule; to remove it use delete_noisy_operation_rule."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name to add the rule to.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Short rule name, e.g. 'health-checks'.")),
			mcp.WithObject("operation", mcp.Required(),
				mcp.Description("HeadSamplingOperationMatcher: typed config matching the operation (e.g. by http target, span name)."),
				mcp.AdditionalProperties(true),
			),
			mcp.WithNumber("percentage_at_most", mcp.Required(), mcp.Description("0-100 — keep at most this fraction of matching traces.")),
			mcp.WithObject("source_scopes", mcp.Description("Optional SourcesScopes to limit which sources this rule applies to."), mcp.AdditionalProperties(true)),
			mcp.WithString("notes", mcp.Description("Optional notes.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional: rule is created disabled.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createNoisyRuleHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_noisy_operation_rule",
			writeAnno(),
			mcp.WithDescription("MODIFY an EXISTING noisy-operation rule (the DROP-noise kind) in place (found by sampling_id + rule_name) — change its match, percentage, or disabled flag. Does NOT create a rule; use create_noisy_operation_rule to add a new one, or delete_noisy_operation_rule to remove."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule name to update.")),
			mcp.WithObject("operation", mcp.Description("Replace operation matcher."), mcp.AdditionalProperties(true)),
			mcp.WithNumber("percentage_at_most", mcp.Description("Replace percentageAtMost.")),
			mcp.WithObject("source_scopes", mcp.Description("Replace SourcesScopes."), mcp.AdditionalProperties(true)),
			mcp.WithString("notes", mcp.Description("Replace notes.")),
			mcp.WithBoolean("disabled", mcp.Description("Toggle disabled.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateNoisyRuleHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_noisy_operation_rule",
			writeAnno(),
			mcp.WithDescription("REMOVE an EXISTING noisy-operation rule (the DROP-noise kind) from a sampling group (by sampling_id + rule_name). Deletes only the one rule, not the group. To edit instead of remove, use update_noisy_operation_rule."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule name to delete.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteNoisyRuleHandler(),
	)

	// Highly-relevant operation rules — sampling_highly_relevant.go
	s.AddTool(
		mcp.NewTool("create_highly_relevant_operation_rule",
			writeAnno(),
			mcp.WithDescription("ADD a NEW highly-relevant rule to an existing sampling group: ALWAYS KEEP the traces you care about most (errors, slow/long requests), keeping AT LEAST percentage_at_least%. OPPOSITE intent of noisy/cost rules, which drop. Requires a group — call create_sampling_group first if none exists. Use update_/delete_highly_relevant_operation_rule to change or remove."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Short rule name.")),
			mcp.WithBoolean("error", mcp.Description("Match traces with an error span.")),
			mcp.WithNumber("duration_at_least_ms", mcp.Description("Match traces longer than this duration.")),
			mcp.WithObject("operation", mcp.Description("TailSamplingOperationMatcher — additional matching by name/attributes."), mcp.AdditionalProperties(true)),
			mcp.WithNumber("percentage_at_least", mcp.Description("0-100 — keep at least this fraction.")),
			mcp.WithObject("source_scopes", mcp.Description("Optional SourcesScopes."), mcp.AdditionalProperties(true)),
			mcp.WithString("notes", mcp.Description("Optional.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createHROHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_highly_relevant_operation_rule",
			writeAnno(),
			mcp.WithDescription("MODIFY an EXISTING highly-relevant rule (the ALWAYS-KEEP kind) in place (found by sampling_id + rule_name). Does NOT create a rule; use create_highly_relevant_operation_rule to add a new one, or delete_highly_relevant_operation_rule to remove."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule name.")),
			mcp.WithBoolean("error", mcp.Description("Replace.")),
			mcp.WithNumber("duration_at_least_ms", mcp.Description("Replace.")),
			mcp.WithObject("operation", mcp.Description("Replace."), mcp.AdditionalProperties(true)),
			mcp.WithNumber("percentage_at_least", mcp.Description("Replace.")),
			mcp.WithObject("source_scopes", mcp.Description("Replace."), mcp.AdditionalProperties(true)),
			mcp.WithString("notes", mcp.Description("Replace.")),
			mcp.WithBoolean("disabled", mcp.Description("Toggle.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateHROHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_highly_relevant_operation_rule",
			writeAnno(),
			mcp.WithDescription("REMOVE an EXISTING highly-relevant rule (the ALWAYS-KEEP kind) from a sampling group (by sampling_id + rule_name). Deletes only the one rule, not the group. To edit instead of remove, use update_highly_relevant_operation_rule."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteHROHandler(),
	)

	// Cost-reduction rules — sampling_cost_reduction.go
	s.AddTool(
		mcp.NewTool("create_cost_reduction_rule",
			writeAnno(),
			mcp.WithDescription("ADD a NEW cost-reduction rule to an existing sampling group: cut telemetry SPEND by down-sampling a high-volume operation to AT MOST percentage_at_most%. Same drop mechanism as noisy, but the intent is cost (use this when the goal is to reduce bill/volume rather than filter health-check noise). Requires a group — call create_sampling_group first if none exists. Use update_/delete_cost_reduction_rule to change or remove."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Short rule name.")),
			mcp.WithObject("operation", mcp.Required(), mcp.Description("TailSamplingOperationMatcher."), mcp.AdditionalProperties(true)),
			mcp.WithNumber("percentage_at_most", mcp.Required(), mcp.Description("0-100.")),
			mcp.WithObject("source_scopes", mcp.Description("Optional."), mcp.AdditionalProperties(true)),
			mcp.WithString("notes", mcp.Description("Optional.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createCRRHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_cost_reduction_rule",
			writeAnno(),
			mcp.WithDescription("MODIFY an EXISTING cost-reduction rule in place (found by sampling_id + rule_name). Does NOT create a rule; use create_cost_reduction_rule to add a new one, or delete_cost_reduction_rule to remove."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule name.")),
			mcp.WithObject("operation", mcp.Description("Replace."), mcp.AdditionalProperties(true)),
			mcp.WithNumber("percentage_at_most", mcp.Description("Replace.")),
			mcp.WithObject("source_scopes", mcp.Description("Replace."), mcp.AdditionalProperties(true)),
			mcp.WithString("notes", mcp.Description("Replace.")),
			mcp.WithBoolean("disabled", mcp.Description("Toggle.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateCRRHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_cost_reduction_rule",
			writeAnno(),
			mcp.WithDescription("REMOVE an EXISTING cost-reduction rule from a sampling group (by sampling_id + rule_name). Deletes only the one rule, not the group. To edit instead of remove, use update_cost_reduction_rule."),
			mcp.WithString("sampling_id", mcp.Required(), mcp.Description("Group CR name.")),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteCRRHandler(),
	)
}

// ---- group handlers --------------------------------------------------------

func listSamplingGroupsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := env.GetCurrentNamespace()
		list, err := kube.DefaultClient.OdigosClient.Samplings(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sampling: %v", err)), nil
		}
		type group struct {
			ResourceName, Name             string
			NoisyCount, HROCount, CRRCount int
		}
		out := make([]group, 0, len(list.Items))
		for _, s := range list.Items {
			out = append(out, group{
				ResourceName: s.Name, Name: s.Spec.Name,
				NoisyCount: len(s.Spec.NoisyOperations),
				HROCount:   len(s.Spec.HighlyRelevantOperations),
				CRRCount:   len(s.Spec.CostReductionRules),
			})
		}
		return withGuidance(out, fmt.Sprintf("%d sampling groups.", len(out)),
			[]NextStep{
				{When: "to inspect one group's rules", Tool: "get_sampling_group"},
				{When: "to create a new group", Tool: "create_sampling_group"},
			})
	}
}

func getSamplingGroupHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("sampling_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ns := env.GetCurrentNamespace()
		var cr odigosv1.Sampling
		if err := kube.CacheClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: id}, &cr); err != nil {
			if apierrors.IsNotFound(err) {
				return mcp.NewToolResultError(fmt.Sprintf("sampling group %q not found", id)), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("get sampling: %v", err)), nil
		}
		return withGuidance(cr, "All three rule arrays returned. Identify a rule by its Name when calling update/delete.",
			[]NextStep{
				{When: "to add a noisy rule", Tool: "create_noisy_operation_rule", Args: map[string]any{"sampling_id": id}},
				{When: "to add a highly-relevant rule", Tool: "create_highly_relevant_operation_rule", Args: map[string]any{"sampling_id": id}},
				{When: "to add a cost-reduction rule", Tool: "create_cost_reduction_rule", Args: map[string]any{"sampling_id": id}},
			})
	}
}

func createSamplingGroupHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()
		cr := &odigosv1.Sampling{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "odigos-mcp-sampling-", Namespace: ns},
			Spec: odigosv1.SamplingSpec{
				Name: name, Notes: stringArg(req, "notes", ""),
			},
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "create_sampling_group", "name": name}, "Dry-run.", nil)
		}
		created, err := kube.DefaultClient.OdigosClient.Samplings(ns).Create(ctx, cr, metav1.CreateOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create sampling group: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "created", "sampling_id": created.Name, "name": created.Spec.Name},
			"Group created (empty). Add rules to populate it.", []NextStep{
				{When: "to add a noisy rule", Tool: "create_noisy_operation_rule", Args: map[string]any{"sampling_id": created.Name}},
			})
	}
}

func deleteSamplingGroupHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("sampling_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()
		if _, err := kube.DefaultClient.OdigosClient.Samplings(ns).Get(ctx, id, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return withGuidance(map[string]any{"action": "noop"}, "Group not found.", nil)
			}
			return mcp.NewToolResultError(fmt.Sprintf("get sampling: %v", err)), nil
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "delete_sampling_group", "sampling_id": id}, "Dry-run: would delete the group and ALL its rules.", nil)
		}
		if err := kube.DefaultClient.OdigosClient.Samplings(ns).Delete(ctx, id, metav1.DeleteOptions{}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete sampling: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "sampling_id": id}, "Group deleted.", nil)
	}
}
