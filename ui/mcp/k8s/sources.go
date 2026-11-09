package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/describe"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
)

// Sources are the user-facing "I want to instrument this workload" CRD. These
// tools cover create/update/delete + the synthesis describe_source aggregate
// the UI shows.
func registerSourceTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("list_sources",
			readAnno(),
			mcp.WithDescription("List all Odigos Sources (instrumented workloads) across the cluster. Returns workload kind/name/namespace and whether instrumentation is disabled. For deep per-workload analysis use describe_source."),
			mcp.WithString("namespace", mcp.Description("Optional namespace filter. Omit for all namespaces.")),
		),
		listSourcesHandler(),
	)

	s.AddTool(
		mcp.NewTool("describe_source",
			readAnno(),
			mcp.WithDescription("Synthesis aggregate for one workload: instrumentation status, runtime detection (language/version), agent enabling/reason per container, pod rollout state, per-pod instrumentation instances. This is the primary 'why is this workload not producing telemetry?' tool. Large response — for narrower questions use get_runtime_detection or get_instrumentation_health."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace of the workload.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind: Deployment, DaemonSet, or StatefulSet.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		),
		describeSourceHandler(deps),
	)

	s.AddTool(
		mcp.NewTool("instrument_source",
			writeAnno(),
			mcp.WithDescription("Instrument a workload by creating a Source CRD. WARNING: when first instrumented, pods are restarted by the controller to inject the agent (community distros; enterprise eBPF distros can attach without restart)."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		instrumentSourceHandler(),
	)

	s.AddTool(
		mcp.NewTool("uninstrument_source",
			writeAnno(),
			mcp.WithDescription("Uninstrument a workload by deleting its Source CRD. WARNING: pods are restarted to remove the agent and telemetry stops flowing."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		uninstrumentSourceHandler(),
	)

	s.AddTool(
		mcp.NewTool("instrument_namespace",
			writeAnno(),
			mcp.WithDescription("Bulk-instrument a whole namespace by creating a Source CR of kind=Namespace. Every workload in the namespace becomes instrumented (unless individually opted out). WARNING: every workload restarts its pods to inject the agent (community distros)."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace name to instrument.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		instrumentNamespaceHandler(),
	)
	s.AddTool(
		mcp.NewTool("uninstrument_namespace",
			writeAnno(),
			mcp.WithDescription("Remove namespace-level instrumentation by deleting the Source CR of kind=Namespace. Workloads with their own Source CR remain instrumented."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace name to remove from instrumentation.")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		uninstrumentNamespaceHandler(),
	)
	s.AddTool(
		mcp.NewTool("update_source",
			writeAnno(),
			mcp.WithDescription("Update a Source CRD: override the OTel service name, or override the distro for a container (e.g. switch a Java container from community to java-enterprise eBPF). Pass only the fields you want to change."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Source CR namespace (same as the workload).")),
			mcp.WithString("source_name", mcp.Required(), mcp.Description("Source resource name (from list_sources).")),
			mcp.WithString("otel_service_name", mcp.Description("Optional: override the OTel service name reported by the agent.")),
			mcp.WithBoolean("disable_instrumentation", mcp.Description("Optional: pause/resume instrumentation without deleting the Source.")),
			mcp.WithArray("container_overrides",
				mcp.Description("Optional: array of {containerName, runtimeInfo:{language, runtimeVersion}, otelDistroName} entries to override per-container detection or distro."),
				mcp.Items(map[string]any{"type": "object"}),
			),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		updateSourceHandler(),
	)
}

func listSourcesHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns := stringArg(req, "namespace", "")
		list, err := kube.DefaultClient.OdigosClient.Sources(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sources: %v", err)), nil
		}
		type row struct {
			Namespace              string `json:"namespace"`
			Name                   string `json:"name"`
			WorkloadKind           string `json:"workloadKind"`
			WorkloadName           string `json:"workloadName"`
			WorkloadNamespace      string `json:"workloadNamespace"`
			DisableInstrumentation bool   `json:"disableInstrumentation"`
			OtelServiceName        string `json:"otelServiceName,omitempty"`
		}
		out := make([]row, 0, len(list.Items))
		for _, src := range list.Items {
			out = append(out, row{
				Namespace:              src.Namespace,
				Name:                   src.Name,
				WorkloadKind:           string(src.Spec.Workload.Kind),
				WorkloadName:           src.Spec.Workload.Name,
				WorkloadNamespace:      src.Spec.Workload.Namespace,
				DisableInstrumentation: src.Spec.DisableInstrumentation,
				OtelServiceName:        src.Spec.OtelServiceName,
			})
		}
		steps := []NextStep{
			{When: "to deep-dive one workload", Tool: "describe_source"},
			{When: "to see live throughput per source", Tool: "get_overview_metrics"},
			{When: "to mark a new workload for instrumentation", Tool: "instrument_source"},
		}
		return withGuidance(out, fmt.Sprintf("Found %d sources.", len(out)), steps)
	}
}

func describeSourceHandler(deps Deps) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kindStr, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var analyze any
		k := normalizeKind(kindStr)
		switch k {
		case k8sconsts.WorkloadKindDeployment:
			analyze, err = describe.DescribeDeployment(ctx, kube.DefaultClient, kube.DefaultClient.OdigosClient, ns, name)
		case k8sconsts.WorkloadKindDaemonSet:
			analyze, err = describe.DescribeDaemonSet(ctx, kube.DefaultClient, kube.DefaultClient.OdigosClient, ns, name)
		case k8sconsts.WorkloadKindStatefulSet:
			analyze, err = describe.DescribeStatefulSet(ctx, kube.DefaultClient, kube.DefaultClient.OdigosClient, ns, name)
		default:
			return errorWithOptions(fmt.Sprintf("unsupported kind %q", kindStr), []string{"Deployment", "DaemonSet", "StatefulSet"})
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("describe %s/%s in %s: %v", kindStr, name, ns, err)), nil
		}
		steps := []NextStep{
			{When: "to see only the runtime detection details", Tool: "get_runtime_detection",
				Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
			{When: "to see per-pod SDK/eBPF component health", Tool: "get_instrumentation_health",
				Args: map[string]any{"namespace": ns}},
			{When: "to retry instrumentation by restarting the pods", Tool: "restart_workloads",
				Args: map[string]any{"sources": []map[string]any{{"namespace": ns, "kind": kindStr, "name": name}}}},
			{When: "to recover after the instrumentation caused a crash and was rolled back", Tool: "recover_from_rollback",
				Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
		}
		return withGuidance(analyze, "describe_source aggregates Source + InstrumentationConfig + InstrumentationInstances + pod state. Look at status conditions for failure reasons (e.g. UnsupportedRuntimeVersion, MissingDistroParameter).", steps)
	}
}

func instrumentSourceHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kindStr, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		k := normalizeKind(kindStr)
		if k == "" {
			return errorWithOptions(fmt.Sprintf("invalid kind %q", kindStr), []string{"Deployment", "DaemonSet", "StatefulSet"})
		}
		dryRun := boolArg(req, "dry_run", true)

		selector := labels.SelectorFromSet(labels.Set{
			k8sconsts.WorkloadNameLabel:      name,
			k8sconsts.WorkloadNamespaceLabel: ns,
			k8sconsts.WorkloadKindLabel:      string(k),
		})
		existing, err := kube.DefaultClient.OdigosClient.Sources(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list existing sources: %v", err)), nil
		}

		if len(existing.Items) > 0 {
			src := &existing.Items[0]
			if !src.Spec.DisableInstrumentation {
				return withGuidance(map[string]any{
					"action": "noop", "source_name": src.Name,
				}, "Workload already instrumented; no change needed.", []NextStep{
					{When: "to inspect it", Tool: "describe_source", Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
				})
			}
			if dryRun {
				return withGuidance(map[string]any{
					"action": "patch_source", "would_change": "DisableInstrumentation: true -> false", "source_name": src.Name, "will_restart_pods": true,
				}, "Dry-run: would re-enable instrumentation on the existing Source.", []NextStep{
					{When: "to actually apply", Tool: "instrument_source", Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name, "dry_run": false}},
				})
			}
			src.Spec.DisableInstrumentation = false
			updated, err := kube.DefaultClient.OdigosClient.Sources(ns).Update(ctx, src, metav1.UpdateOptions{})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("re-enable source: %v", err)), nil
			}
			return withGuidance(map[string]any{
				"action": "patched", "source_name": updated.Name, "will_restart_pods": true,
			}, "Source re-enabled; controller will restart pods to inject the agent.", nil)
		}

		newSrc := &odigosv1.Source{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: workload.CalculateWorkloadRuntimeObjectName(name, k),
				Namespace:    ns,
			},
			Spec: odigosv1.SourceSpec{
				Workload: k8sconsts.PodWorkload{Kind: k, Name: name, Namespace: ns},
			},
		}
		if dryRun {
			return withGuidance(map[string]any{
				"action": "create_source", "workload_kind": string(k), "workload_name": name, "will_restart_pods": true,
			}, "Dry-run: would create a new Source CRD.", []NextStep{
				{When: "to actually apply", Tool: "instrument_source", Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name, "dry_run": false}},
			})
		}
		created, err := kube.DefaultClient.OdigosClient.Sources(ns).Create(ctx, newSrc, metav1.CreateOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create source: %v", err)), nil
		}
		return withGuidance(map[string]any{
			"action": "created", "source_name": created.Name, "namespace": created.Namespace, "will_restart_pods": true,
		}, "Created. Pods will restart to inject the agent (community distros).", []NextStep{
			{When: "after ~30s, to verify instrumentation came up", Tool: "describe_source",
				Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
		})
	}
}

func uninstrumentSourceHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kindStr, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		k := normalizeKind(kindStr)
		dryRun := boolArg(req, "dry_run", true)

		selector := labels.SelectorFromSet(labels.Set{
			k8sconsts.WorkloadNameLabel:      name,
			k8sconsts.WorkloadNamespaceLabel: ns,
			k8sconsts.WorkloadKindLabel:      string(k),
		})
		existing, err := kube.DefaultClient.OdigosClient.Sources(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list sources: %v", err)), nil
		}
		if len(existing.Items) == 0 {
			return withGuidance(map[string]any{"action": "noop"}, "No Source CRD for this workload — nothing to remove.", nil)
		}
		src := &existing.Items[0]
		if dryRun {
			return withGuidance(map[string]any{
				"action": "delete_source", "source_name": src.Name, "will_restart_pods": true,
			}, "Dry-run: would delete the Source CRD; pods restart, telemetry stops.", nil)
		}
		if err := kube.DefaultClient.OdigosClient.Sources(ns).Delete(ctx, src.Name, metav1.DeleteOptions{}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete source: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "source_name": src.Name}, "Source deleted; pods restart, telemetry stops.", nil)
	}
}

func instrumentNamespaceHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)

		// Namespace-kind Source: spec.workload.kind=Namespace, name=ns, namespace=ns.
		selector := labels.SelectorFromSet(labels.Set{
			k8sconsts.WorkloadNameLabel:      ns,
			k8sconsts.WorkloadNamespaceLabel: ns,
			k8sconsts.WorkloadKindLabel:      string(k8sconsts.WorkloadKindNamespace),
		})
		existing, err := kube.DefaultClient.OdigosClient.Sources(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list ns sources: %v", err)), nil
		}
		if len(existing.Items) > 0 {
			return withGuidance(map[string]any{"action": "noop", "source_name": existing.Items[0].Name},
				"Namespace already instrumented.", nil)
		}
		newSrc := &odigosv1.Source{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "odigos-mcp-ns-", Namespace: ns},
			Spec: odigosv1.SourceSpec{
				Workload: k8sconsts.PodWorkload{Kind: k8sconsts.WorkloadKindNamespace, Name: ns, Namespace: ns},
			},
		}
		if dryRun {
			return withGuidance(map[string]any{"action": "create_namespace_source", "namespace": ns},
				"Dry-run: would instrument every workload in the namespace.", []NextStep{
					{When: "to apply", Tool: "instrument_namespace", Args: map[string]any{"namespace": ns, "dry_run": false}},
				})
		}
		created, err := kube.DefaultClient.OdigosClient.Sources(ns).Create(ctx, newSrc, metav1.CreateOptions{})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("create namespace source: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "created", "source_name": created.Name, "namespace": ns},
			"Namespace marked for instrumentation; every workload's pods will restart for agent injection (community distros).", nil)
	}
}

func uninstrumentNamespaceHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		selector := labels.SelectorFromSet(labels.Set{
			k8sconsts.WorkloadNameLabel:      ns,
			k8sconsts.WorkloadNamespaceLabel: ns,
			k8sconsts.WorkloadKindLabel:      string(k8sconsts.WorkloadKindNamespace),
		})
		existing, err := kube.DefaultClient.OdigosClient.Sources(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list ns sources: %v", err)), nil
		}
		if len(existing.Items) == 0 {
			return withGuidance(map[string]any{"action": "noop"}, "No namespace-kind Source found.", nil)
		}
		src := &existing.Items[0]
		if dryRun {
			return withGuidance(map[string]any{"action": "delete_namespace_source", "source_name": src.Name}, "Dry-run.", nil)
		}
		if err := kube.DefaultClient.OdigosClient.Sources(ns).Delete(ctx, src.Name, metav1.DeleteOptions{}); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("delete namespace source: %v", err)), nil
		}
		return withGuidance(map[string]any{"action": "deleted", "source_name": src.Name},
			"Namespace-level instrumentation removed. Workload-level Sources continue to apply.", nil)
	}
}

func updateSourceHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("source_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)

		// Re-fetch + re-apply on a resourceVersion conflict so two concurrent
		// edits to the same Source don't 409.
		var done *mcp.CallToolResult
		applyErr := retry.RetryOnConflict(conflictBackoff, func() error {
			src, err := kube.DefaultClient.OdigosClient.Sources(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					done = mcp.NewToolResultError(fmt.Sprintf("source %q not found in %s", name, ns))
					return nil
				}
				return err
			}

			changes := map[string]any{}
			if v := stringArg(req, "otel_service_name", ""); v != "" {
				changes["otelServiceName"] = v
				src.Spec.OtelServiceName = v
			}
			if dp := boolArgPtr(req, "disable_instrumentation"); dp != nil {
				changes["disableInstrumentation"] = *dp
				src.Spec.DisableInstrumentation = *dp
			}
			var overrides []odigosv1.ContainerOverride
			if ok, err := decodeArg(req, "container_overrides", &overrides); err != nil {
				done = mcp.NewToolResultError(err.Error())
				return nil
			} else if ok {
				changes["containerOverrides"] = overrides
				src.Spec.ContainerOverrides = overrides
			}

			if len(changes) == 0 {
				done, _ = withGuidance(map[string]any{"action": "noop"}, "Nothing to change; supply at least one of otel_service_name, disable_instrumentation, container_overrides.", nil)
				return nil
			}

			if dryRun {
				done, _ = withGuidance(map[string]any{"action": "update_source", "source_name": name, "would_change": changes},
					"Dry-run: would update the Source CR.", []NextStep{
						{When: "to actually apply", Tool: "update_source", Args: map[string]any{"namespace": ns, "source_name": name, "dry_run": false}},
					})
				return nil
			}
			updated, err := kube.DefaultClient.OdigosClient.Sources(ns).Update(ctx, src, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			done, _ = withGuidance(map[string]any{"action": "updated", "source_name": updated.Name},
				"Updated. Distro/runtime changes may trigger a pod rollout.", nil)
			return nil
		})
		if applyErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("update source: %v", applyErr)), nil
		}
		return done, nil
	}
}
