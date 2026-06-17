package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// registerCustomInstrumentationTools is the "add a span on a function" write —
// the closing step of the profile -> hot function -> probe loop, and the
// "missing data on demand" demo Eden asked for.
func registerCustomInstrumentationTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("add_custom_instrumentation",
			writeAnno(),
			mcp.WithDescription("Add a custom eBPF probe to instrument a specific function/method without changing application code. Typically called after get_source_hot_functions identifies where time is spent, or when telemetry from a specific symbol is missing. Languages: 'java' (class + method), 'go' (package + function, or package + receiver + receiver method), 'cpp' (signature). Optionally scope to a single workload."),
			mcp.WithString("language", mcp.Required(), mcp.Description("One of: java, go, cpp.")),
			mcp.WithString("rule_name", mcp.Description("Optional human-readable name for the rule.")),
			mcp.WithString("class_name", mcp.Description("java: fully-qualified class name.")),
			mcp.WithString("method_name", mcp.Description("java: method name to instrument.")),
			mcp.WithString("package_name", mcp.Description("go: package import path, e.g. net/http (required for go).")),
			mcp.WithString("function_name", mcp.Description("go: function name. Use this OR receiver_name + receiver_method_name.")),
			mcp.WithString("receiver_name", mcp.Description("go: receiver struct name. Requires receiver_method_name.")),
			mcp.WithString("receiver_method_name", mcp.Description("go: method name on the receiver.")),
			mcp.WithString("signature", mcp.Description("cpp: function signature, e.g. std::vector::push_back or SSL_write.")),
			mcp.WithString("workload_namespace", mcp.Description("Optional: scope to a workload in this namespace (requires workload_kind and workload_name).")),
			mcp.WithString("workload_kind", mcp.Description("Optional: Deployment, DaemonSet, or StatefulSet.")),
			mcp.WithString("workload_name", mcp.Description("Optional: workload name to scope to.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		addCustomInstrumentationHandler(deps),
	)
}

func addCustomInstrumentationHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		language, err := req.RequireString("language")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)

		ci := &instrumentationrules.CustomInstrumentations{}
		var probeSummary string

		switch language {
		case "java":
			probe := instrumentationrules.JavaCustomProbe{
				ClassName:  stringArg(req, "class_name", ""),
				MethodName: stringArg(req, "method_name", ""),
			}
			if err := probe.Verify(); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid java probe: %v", err)), nil
			}
			ci.Java = []instrumentationrules.JavaCustomProbe{probe}
			probeSummary = fmt.Sprintf("%s.%s", probe.ClassName, probe.MethodName)
		case "go":
			probe := instrumentationrules.GolangCustomProbe{
				PackageName:        stringArg(req, "package_name", ""),
				FunctionName:       stringArg(req, "function_name", ""),
				ReceiverName:       stringArg(req, "receiver_name", ""),
				ReceiverMethodName: stringArg(req, "receiver_method_name", ""),
			}
			if err := probe.Verify(); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid go probe: %v", err)), nil
			}
			ci.Golang = []instrumentationrules.GolangCustomProbe{probe}
			if probe.FunctionName != "" {
				probeSummary = fmt.Sprintf("%s.%s", probe.PackageName, probe.FunctionName)
			} else {
				probeSummary = fmt.Sprintf("%s.(%s).%s", probe.PackageName, probe.ReceiverName, probe.ReceiverMethodName)
			}
		case "cpp":
			probe := instrumentationrules.CppCustomProbe{Signature: stringArg(req, "signature", "")}
			if err := probe.Verify(); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid cpp probe: %v", err)), nil
			}
			ci.Cpp = []instrumentationrules.CppCustomProbe{probe}
			probeSummary = probe.Signature
		default:
			return errorWithOptions(fmt.Sprintf("unknown language %q", language), []string{"java", "go", "cpp"})
		}

		scope, err := parseOptionalWorkloadScope(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		ns := env.GetCurrentNamespace()

		if dryRun {
			steps := []NextStep{
				{When: "to actually create the rule", Tool: "add_custom_instrumentation",
					Args: map[string]any{"language": language, "dry_run": false}},
			}
			return withGuidance(map[string]any{
				"action":           "create_custom_instrumentation",
				"odigos_namespace": ns,
				"language":         language,
				"probe":            probeSummary,
				"scoped":           scope != nil,
				"dry_run":          true,
			}, fmt.Sprintf("Dry-run validated. Would create a custom %s probe on %s.", language, probeSummary), steps)
		}

		rule := &odigosv1.InstrumentationRule{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "odigos-mcp-custom-",
				Namespace:    ns,
			},
			Spec: odigosv1.InstrumentationRuleSpec{
				RuleName:               stringArg(req, "rule_name", ""),
				CustomInstrumentations: ci,
				Scopes:                 scope,
			},
		}
		created, err := kube.DefaultClient.OdigosClient.InstrumentationRules(ns).Create(ctx, rule, metav1.CreateOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create custom instrumentation rule: %v", err)), nil
		}
		steps := []NextStep{
			{When: "to verify the rule is live", Tool: "list_instrumentation_rules"},
			{When: "to inspect the workload's agent health after the probe attaches", Tool: "get_instrumentation_health"},
		}
		return withGuidance(map[string]any{
			"action":    "created",
			"rule_name": created.Name,
			"namespace": created.Namespace,
			"language":  language,
			"probe":     probeSummary,
		}, fmt.Sprintf("Created custom %s probe on %s. The agent will pick it up on next reconcile (often without restart on enterprise eBPF distros).", language, probeSummary), steps)
	}
}
