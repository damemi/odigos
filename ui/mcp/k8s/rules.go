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

// InstrumentationRules tune the agent's behavior dynamically: what HTTP
// headers/bodies to collect, what code attributes to emit, whether to switch
// the logs pipeline to eBPF, whether to disable tracing on a scope, head
// sampling at agent level, per-library distros, trace verbosity. This is the
// primary "get more data on demand" surface Eden asked for; pair with
// add_custom_instrumentation for symbol-level probes.
//
// This file owns the read surface + tool registration. Create / update +
// their supporting parsers (replaceRuleSubConfig + applyRuleAdvancedFields)
// live in rules_write.go.

func registerRuleTools(s *server.MCPServer, deps Deps) {
	// Reads
	s.AddTool(
		mcp.NewTool("list_instrumentation_rules",
			readAnno(),
			mcp.WithDescription("List InstrumentationRules in the Odigos namespace. Each row reports which families it sets (payloadCollection, headersCollection, customInstrumentations, codeAttributes, traceConfig, ebpfLogCapture, headSamplingFallbackFraction, otelDistros, traceVerbosity) and its scope."),
		),
		listInstrumentationRulesHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_instrumentation_rule",
			readAnno(),
			mcp.WithDescription("Return one InstrumentationRule with its full spec — all populated rule families and the SourcesScopes / InstrumentationLibraries scope."),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule resource name from list_instrumentation_rules.")),
		),
		getInstrumentationRuleHandler(),
	)

	// Writes — see rules_write.go
	s.AddTool(
		mcp.NewTool("create_instrumentation_rule",
			writeAnno(),
			mcp.WithDescription("Create an InstrumentationRule to collect more data dynamically without changing application code. rule_type: 'headers' (collect HTTP header keys), 'payload' (collect HTTP request/response, DB query, or messaging/Kafka payloads), 'code_attributes' (toggle code.* span attributes), 'trace_config' (turn tracing on/off for a scope), 'ebpf_log_capture' (switch logs pipeline to eBPF). Optionally scope to one workload. For custom probes on specific functions, use add_custom_instrumentation instead."),
			mcp.WithString("rule_type", mcp.Required(), mcp.Description("One of: headers, payload, code_attributes, trace_config, ebpf_log_capture.")),
			mcp.WithString("rule_name", mcp.Description("Optional human-readable name.")),
			// headers
			mcp.WithArray("header_keys",
				mcp.Description("headers: HTTP header keys to collect, e.g. [\"X-Trace-Id\"]."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			// payload
			mcp.WithBoolean("http_request", mcp.Description("payload: collect HTTP request body.")),
			mcp.WithBoolean("http_response", mcp.Description("payload: collect HTTP response body.")),
			mcp.WithBoolean("db_query", mcp.Description("payload: collect DB query payloads.")),
			mcp.WithBoolean("messaging", mcp.Description("payload: collect messaging/Kafka payloads.")),
			mcp.WithNumber("max_payload_length", mcp.Description("payload: optional max payload length (bytes) applied to every enabled payload collector.")),
			// code_attributes
			mcp.WithBoolean("code_column", mcp.Description("code_attributes: record code.column.")),
			mcp.WithBoolean("code_filepath", mcp.Description("code_attributes: record code.filepath.")),
			mcp.WithBoolean("code_function", mcp.Description("code_attributes: record code.function.")),
			mcp.WithBoolean("code_lineno", mcp.Description("code_attributes: record code.lineno.")),
			mcp.WithBoolean("code_namespace", mcp.Description("code_attributes: record code.namespace.")),
			mcp.WithBoolean("code_stacktrace", mcp.Description("code_attributes: record code.stacktrace.")),
			// trace_config
			mcp.WithBoolean("trace_disabled", mcp.Description("trace_config: disable tracing for the scope.")),
			// ebpf_log_capture
			mcp.WithBoolean("ebpf_log_capture_enabled", mcp.Description("ebpf_log_capture: enable eBPF-based log capture (replaces filelog receiver).")),
			// Advanced fields — independent of rule_type and may be combined.
			mcp.WithNumber("head_sampling_fraction", mcp.Description("Optional: agent-side head sampling fraction in [0,1]. 1=keep all, 0=drop all (unless a rule matches).")),
			mcp.WithArray("otel_distros",
				mcp.Description("Optional: override default distros for the scoped workloads, e.g. [\"java-enterprise\",\"golang-enterprise\"]."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			mcp.WithArray("trace_verbosity_disabled_libraries",
				mcp.Description("Optional: array of {Language, libraryName} — disable trace collection on these libraries (e.g. turn off net/http for Go)."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithArray("trace_verbosity_enabled_libraries",
				mcp.Description("Optional: array of {Language, libraryName} — enable libraries that are disabled by default (e.g. nodejs fs/dns/net)."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithArray("instrumentation_libraries",
				mcp.Description("Optional: scope this rule to specific instrumentation libraries — array of {name, language, spanKind?}. nil = applies to all libraries."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			// scope
			mcp.WithString("workload_namespace", mcp.Description("Optional: scope to a workload's namespace (requires workload_kind and workload_name).")),
			mcp.WithString("workload_kind", mcp.Description("Optional: Deployment, DaemonSet, or StatefulSet.")),
			mcp.WithString("workload_name", mcp.Description("Optional: workload name to scope to.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		createInstrumentationRuleHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_instrumentation_rule",
			writeAnno(),
			mcp.WithDescription("Update an existing InstrumentationRule. Pass rule_name plus only the params you want to change. The rule_type acts the same as in create_instrumentation_rule and REPLACES the named sub-config; if rule_type is omitted, only labels/scope/disabled change. To set the advanced fields, pass head_sampling_fraction, otel_distros[], or trace_verbosity_disabled_libraries / trace_verbosity_enabled_libraries directly."),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule resource name to update.")),
			mcp.WithString("new_rule_label", mcp.Description("Optional: change the spec.ruleName label.")),
			mcp.WithBoolean("disabled", mcp.Description("Optional: temporarily disable/enable.")),
			// rule_type-keyed bodies (replace the named sub-config)
			mcp.WithString("rule_type", mcp.Description("Optional: headers | payload | code_attributes | trace_config | ebpf_log_capture.")),
			mcp.WithArray("header_keys", mcp.Description("rule_type=headers."), mcp.Items(map[string]any{"type": "string"})),
			mcp.WithBoolean("http_request", mcp.Description("rule_type=payload.")),
			mcp.WithBoolean("http_response", mcp.Description("rule_type=payload.")),
			mcp.WithBoolean("db_query", mcp.Description("rule_type=payload.")),
			mcp.WithBoolean("messaging", mcp.Description("rule_type=payload.")),
			mcp.WithNumber("max_payload_length", mcp.Description("rule_type=payload.")),
			mcp.WithBoolean("code_column", mcp.Description("rule_type=code_attributes.")),
			mcp.WithBoolean("code_filepath", mcp.Description("rule_type=code_attributes.")),
			mcp.WithBoolean("code_function", mcp.Description("rule_type=code_attributes.")),
			mcp.WithBoolean("code_lineno", mcp.Description("rule_type=code_attributes.")),
			mcp.WithBoolean("code_namespace", mcp.Description("rule_type=code_attributes.")),
			mcp.WithBoolean("code_stacktrace", mcp.Description("rule_type=code_attributes.")),
			mcp.WithBoolean("trace_disabled", mcp.Description("rule_type=trace_config.")),
			mcp.WithBoolean("ebpf_log_capture_enabled", mcp.Description("rule_type=ebpf_log_capture.")),
			// Advanced fields (orthogonal to rule_type)
			mcp.WithNumber("head_sampling_fraction", mcp.Description("Optional: agent-side head sampling fraction in [0,1].")),
			mcp.WithArray("otel_distros",
				mcp.Description("Optional: override default distros, e.g. [\"java-enterprise\",\"golang-enterprise\"]."),
				mcp.Items(map[string]any{"type": "string"}),
			),
			mcp.WithArray("trace_verbosity_disabled_libraries",
				mcp.Description("Optional: list of {Language, libraryName} to disable trace collection."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithArray("trace_verbosity_enabled_libraries",
				mcp.Description("Optional: list of {Language, libraryName} to enable trace collection on libraries disabled by default (e.g. nodejs fs/dns/net)."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			// Scoping
			mcp.WithArray("instrumentation_libraries",
				mcp.Description("Optional: scope the rule to specific instrumentation libraries — array of {name, language, spanKind?}. nil = all libraries; empty = no libraries (rule effectively off)."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithString("workload_namespace", mcp.Description("Optional: re-scope to a workload's namespace.")),
			mcp.WithString("workload_kind", mcp.Description("Optional.")),
			mcp.WithString("workload_name", mcp.Description("Optional.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateInstrumentationRuleHandler(),
	)
	s.AddTool(
		mcp.NewTool("delete_instrumentation_rule",
			writeAnno(),
			mcp.WithDescription("Delete an InstrumentationRule by resource name."),
			mcp.WithString("rule_name", mcp.Required(), mcp.Description("Rule resource name (from list_instrumentation_rules).")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		deleteInstrumentationRuleHandler(),
	)
}

// ---- read + delete handlers ----------------------------------------------

func listInstrumentationRulesHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := env.GetCurrentNamespace()
		list, err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list instrumentation rules: %v", err)), nil
		}
		type row struct {
			Name                   string   `json:"name"`
			RuleName               string   `json:"ruleName"`
			Disabled               bool     `json:"disabled"`
			Families               []string `json:"ruleFamilies"`
			ScopedToNamespaces     []string `json:"scopedToNamespaces,omitempty"`
			ScopedToWorkloadsCount int      `json:"scopedToWorkloadsCount,omitempty"`
		}
		out := make([]row, 0, len(list.Items))
		for _, r := range list.Items {
			families := []string{}
			if r.Spec.PayloadCollection != nil {
				families = append(families, "payloadCollection")
			}
			if r.Spec.HeadersCollection != nil {
				families = append(families, "headersCollection")
			}
			if r.Spec.CustomInstrumentations != nil {
				families = append(families, "customInstrumentations")
			}
			if r.Spec.CodeAttributes != nil {
				families = append(families, "codeAttributes")
			}
			if r.Spec.TraceConfig != nil {
				families = append(families, "traceConfig")
			}
			if r.Spec.EbpfLogCapture != nil {
				families = append(families, "ebpfLogCapture")
			}
			if r.Spec.HeadSamplingFallbackFraction != nil {
				families = append(families, "headSamplingFallbackFraction")
			}
			if r.Spec.OtelDistros != nil {
				families = append(families, "otelDistros")
			}
			if r.Spec.TraceVerbosity != nil {
				families = append(families, "traceVerbosity")
			}
			rr := row{Name: r.Name, RuleName: r.Spec.RuleName, Disabled: r.Spec.Disabled, Families: families}
			if r.Spec.Scopes != nil {
				rr.ScopedToNamespaces = r.Spec.Scopes.Namespaces
				rr.ScopedToWorkloadsCount = len(r.Spec.Scopes.Sources)
			}
			out = append(out, rr)
		}
		return withGuidance(out, fmt.Sprintf("%d rules configured.", len(out)), []NextStep{
			{When: "to add more data collection (headers/body/kafka)", Tool: "create_instrumentation_rule"},
			{When: "to add a span on a specific function", Tool: "add_custom_instrumentation"},
		})
	}
}

func getInstrumentationRuleHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("rule_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ns := env.GetCurrentNamespace()
		var rule odigosv1.InstrumentationRule
		if err := kube.CacheClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &rule); err != nil {
			if apierrors.IsNotFound(err) {
				return mcp.NewToolResultError(fmt.Sprintf("rule %q not found", name)), nil
			}
			return mcp.NewToolResultError(fmt.Sprintf("get rule: %v", err)), nil
		}
		return withGuidance(rule, "Full InstrumentationRuleSpec returned. The populated sub-fields tell you what this rule does.", []NextStep{
			{When: "to change a field", Tool: "update_instrumentation_rule", Args: map[string]any{"rule_name": name}},
		})
	}
}

func deleteInstrumentationRuleHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("rule_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		ns := env.GetCurrentNamespace()
		if _, err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return withGuidance(map[string]any{"action": "noop"}, "Rule not found — nothing to delete.", nil)
			}
			return mcp.NewToolResultError(fmt.Sprintf("get instrumentation rule: %v", err)), nil
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "delete_instrumentation_rule", "rule_name": name}, "Dry-run.", nil)
		}
		if err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete rule: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "rule_name": name}, "Deleted.", nil)
	}
}
