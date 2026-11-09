package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// rules_write.go owns the create / update handlers and their shared parsers:
//
//   - replaceRuleSubConfig     writes a sub-config keyed by rule_type
//     (headers / payload / code_attributes / trace_config / ebpf_log_capture)
//   - applyRuleAdvancedFields  applies the four orthogonal advanced fields
//     (instrumentation_libraries scope, headSamplingFallbackFraction,
//     otelDistros, traceVerbosity)
//
// Both create and update consume the same two helpers, so behavior stays in
// sync as new rule families or knobs are added.

func createInstrumentationRuleHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ruleType, err := req.RequireString("rule_type")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)

		spec := odigosv1.InstrumentationRuleSpec{RuleName: stringArg(req, "rule_name", "")}

		// rule_type-keyed sub-config (validates required params for each kind).
		if err := replaceRuleSubConfig(req, ruleType, &spec); err != nil {
			return errorWithOptions(err.Error(), []string{
				"headers", "payload", "code_attributes", "trace_config", "ebpf_log_capture",
			})
		}

		// Advanced fields apply regardless of rule_type (create doesn't track a diff).
		if err := applyRuleAdvancedFields(req, &spec, nil); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Optional scope to one workload.
		scope, err := parseOptionalWorkloadScope(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		spec.Scopes = scope

		ns := env.GetCurrentNamespace()
		if dryRun {
			return withGuidance(map[string]any{
				"action": "create_instrumentation_rule", "rule_type": ruleType, "scoped": scope != nil,
			}, "Dry-run: rule validated.", []NextStep{
				{When: "to actually apply", Tool: "create_instrumentation_rule",
					Args: map[string]any{"rule_type": ruleType, "dry_run": false}},
			})
		}
		rule := &odigosv1.InstrumentationRule{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "odigos-mcp-rule-", Namespace: ns},
			Spec:       spec,
		}
		created, err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).Create(ctx, rule, metav1.CreateOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create instrumentation rule: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "created", "rule_name": created.Name},
			"Rule created. Agents pick up rules on reconcile — usually within seconds.", []NextStep{
				{When: "to verify it's live", Tool: "list_instrumentation_rules"},
			})
	}
}

func updateInstrumentationRuleHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("rule_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()

		// Re-fetch + re-apply on resourceVersion conflict so concurrent edits to
		// the same rule don't fail under load.
		var done *mcp.CallToolResult
		applyErr := retry.RetryOnConflict(conflictBackoff, func() error {
			rule, err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					done = mcp.NewToolResultError(fmt.Sprintf("rule %q not found", name))
					return nil
				}
				return err
			}

			changed := map[string]any{}
			if v := stringArg(req, "new_rule_label", ""); v != "" {
				rule.Spec.RuleName = v
				changed["ruleName"] = v
			}
			if dp := boolArgPtr(req, "disabled"); dp != nil {
				rule.Spec.Disabled = *dp
				changed["disabled"] = *dp
			}
			if ruleType := stringArg(req, "rule_type", ""); ruleType != "" {
				if err := replaceRuleSubConfig(req, ruleType, &rule.Spec); err != nil {
					done = mcp.NewToolResultError(err.Error())
					return nil
				}
				changed["rule_type"] = ruleType
			}
			if err := applyRuleAdvancedFields(req, &rule.Spec, changed); err != nil {
				done = mcp.NewToolResultError(err.Error())
				return nil
			}
			if scope, err := parseOptionalWorkloadScope(req); err != nil {
				done = mcp.NewToolResultError(err.Error())
				return nil
			} else if scope != nil {
				rule.Spec.Scopes = scope
				changed["sourcesScopes"] = "replaced"
			}

			if len(changed) == 0 {
				done, _ = withGuidance(map[string]any{"action": "noop"}, "Nothing to change; supply at least one updatable field.", nil)
				return nil
			}
			if dryRun {
				done, _ = withGuidance(map[string]any{"action": "update_instrumentation_rule", "rule_name": name, "would_change": changed},
					"Dry-run.", []NextStep{
						{When: "to actually apply", Tool: "update_instrumentation_rule", Args: map[string]any{"rule_name": name, "dry_run": false}},
					})
				return nil
			}
			updated, err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).Update(ctx, rule, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			done, _ = withGuidance(map[string]any{"action": "updated", "rule_name": updated.Name},
				"Rule updated; agents reconcile within seconds.", nil)
			return nil
		})
		if applyErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update rule: %v", applyErr)), nil
		}
		return done, nil
	}
}

// replaceRuleSubConfig writes the sub-config keyed by rule_type. Used by both
// create_instrumentation_rule and update_instrumentation_rule so the
// validation rules stay identical.
func replaceRuleSubConfig(req mcp.CallToolRequest, ruleType string, spec *odigosv1.InstrumentationRuleSpec) error {
	switch ruleType {
	case "headers":
		keys := stringSliceArg(req, "header_keys")
		if len(keys) == 0 {
			return fmt.Errorf("headers requires non-empty header_keys")
		}
		spec.HeadersCollection = &instrumentationrules.HttpHeadersCollection{HeaderKeys: keys}
	case "payload":
		httpReq := boolArg(req, "http_request", false)
		httpResp := boolArg(req, "http_response", false)
		dbQuery := boolArg(req, "db_query", false)
		messaging := boolArg(req, "messaging", false)
		if !httpReq && !httpResp && !dbQuery && !messaging {
			return fmt.Errorf("payload requires at least one of http_request, http_response, db_query, messaging")
		}
		maxLen := int64ArgPtr(req, "max_payload_length")
		pc := &instrumentationrules.PayloadCollection{}
		if httpReq {
			pc.HttpRequest = &instrumentationrules.HttpPayloadCollection{MaxPayloadLength: maxLen}
		}
		if httpResp {
			pc.HttpResponse = &instrumentationrules.HttpPayloadCollection{MaxPayloadLength: maxLen}
		}
		if dbQuery {
			pc.DbQuery = &instrumentationrules.DbQueryPayloadCollection{MaxPayloadLength: maxLen}
		}
		if messaging {
			pc.Messaging = &instrumentationrules.MessagingPayloadCollection{MaxPayloadLength: maxLen}
		}
		spec.PayloadCollection = pc
	case "code_attributes":
		ca := &instrumentationrules.CodeAttributes{
			Column:     boolArgPtr(req, "code_column"),
			FilePath:   boolArgPtr(req, "code_filepath"),
			Function:   boolArgPtr(req, "code_function"),
			LineNumber: boolArgPtr(req, "code_lineno"),
			Namespace:  boolArgPtr(req, "code_namespace"),
			Stacktrace: boolArgPtr(req, "code_stacktrace"),
		}
		if ca.Column == nil && ca.FilePath == nil && ca.Function == nil && ca.LineNumber == nil && ca.Namespace == nil && ca.Stacktrace == nil {
			return fmt.Errorf("code_attributes requires at least one code_* flag")
		}
		spec.CodeAttributes = ca
	case "trace_config":
		disabled := boolArgPtr(req, "trace_disabled")
		if disabled == nil {
			return fmt.Errorf("trace_config requires trace_disabled")
		}
		spec.TraceConfig = &instrumentationrules.TraceConfig{Disabled: disabled}
	case "ebpf_log_capture":
		enabled := boolArgPtr(req, "ebpf_log_capture_enabled")
		if enabled == nil {
			return fmt.Errorf("ebpf_log_capture requires ebpf_log_capture_enabled")
		}
		spec.EbpfLogCapture = &instrumentationrules.EbpfLogCapture{Enabled: enabled}
	default:
		return fmt.Errorf("unknown rule_type %q (one of: headers, payload, code_attributes, trace_config, ebpf_log_capture)", ruleType)
	}
	return nil
}

// applyRuleAdvancedFields handles the four orthogonal-to-rule_type fields:
// instrumentation_libraries scope, headSamplingFallbackFraction, otelDistros,
// traceVerbosity (enabled/disabled libraries). These can be combined with
// any rule_type or applied on their own.
// applyRuleAdvancedFields records every field it sets into changed, so callers
// (notably update) can tell whether an advanced-field-only request actually
// mutated the spec and must be persisted. changed may be nil for create.
func applyRuleAdvancedFields(req mcp.CallToolRequest, spec *odigosv1.InstrumentationRuleSpec, changed map[string]any) error {
	mark := func(k string, v any) {
		if changed != nil {
			changed[k] = v
		}
	}
	// instrumentation_libraries (scoping)
	var libs []odigosv1.InstrumentationLibraryGlobalId
	if ok, err := decodeArg(req, "instrumentation_libraries", &libs); err != nil {
		return err
	} else if ok {
		copyLibs := libs
		spec.InstrumentationLibraries = &copyLibs
		mark("instrumentationLibraries", len(libs))
	}
	// head_sampling_fraction
	if v := floatArg(req, "head_sampling_fraction", -1); v >= 0 {
		spec.HeadSamplingFallbackFraction = &instrumentationrules.HeadSamplingFallbackFraction{FractionToKeep: v}
		mark("headSamplingFallbackFraction", v)
	}
	// otel_distros
	if names := stringSliceArg(req, "otel_distros"); len(names) > 0 {
		spec.OtelDistros = &instrumentationrules.OtelDistros{OtelDistroNames: names}
		mark("otelDistros", names)
	}
	// trace_verbosity_* (combined into one TraceVerbosity).
	var disabled, enabled []instrumentationrules.InstrumentationLibrary
	hasDis, hasEna := false, false
	if ok, err := decodeArg(req, "trace_verbosity_disabled_libraries", &disabled); err != nil {
		return err
	} else if ok {
		hasDis = true
	}
	if ok, err := decodeArg(req, "trace_verbosity_enabled_libraries", &enabled); err != nil {
		return err
	} else if ok {
		hasEna = true
	}
	if hasDis || hasEna {
		spec.TraceVerbosity = &instrumentationrules.TraceVerbosity{DisabledLibraries: disabled, EnabledLibraries: enabled}
		mark("traceVerbosity", true)
	}
	return nil
}
