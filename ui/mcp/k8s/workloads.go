package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/frontend/services"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
)

// Workloads = K8s workloads (Deployment, DaemonSet, StatefulSet, …) regardless of
// instrumentation state. Sources are a subset (workloads with a Source CRD).
func registerWorkloadTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("get_runtime_detection",
			readAnno(),
			mcp.WithDescription("Per-container runtime detection for an instrumented workload: detected programming language, runtime version, libC type, and any detection errors. This is the targeted answer to 'what did Odigos detect for this workload?' — much smaller than describe_source."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind: Deployment, DaemonSet, or StatefulSet.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
		),
		getRuntimeDetectionHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_pod_details",
			readAnno(),
			mcp.WithDescription("Full pod details for any pod (instrumented application pod, collector pod, anything): containers, lifecycle, resources. Use to investigate one specific pod's state. For collector pods specifically, prefer get_collector_pod."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Pod namespace.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Pod name.")),
		),
		getPodDetailsHandler(),
	)
	s.AddTool(
		mcp.NewTool("list_workloads_in_namespace",
			readAnno(),
			mcp.WithDescription("List all workloads (Deployment/StatefulSet/DaemonSet/CronJob/etc.) in a namespace with instance counts. Use during onboarding to see what's available to instrument before calling instrument_source or instrument_namespace. Set include_runtime=true to also get each workload's detected language/runtime-version inline — answer 'what languages are my workloads?' in ONE call instead of calling get_runtime_detection per workload."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Namespace to scan.")),
			mcp.WithBoolean("include_runtime", mcp.Description("If true, inline per-workload runtime detection (language + runtime version per container) so you don't need a separate get_runtime_detection call per workload.")),
		),
		listWorkloadsInNamespaceHandler(),
	)
}

func getRuntimeDetectionHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kindStr, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		k := normalizeKind(kindStr)
		if k == "" {
			return errorWithOptions(fmt.Sprintf("invalid kind %q", kindStr), []string{"Deployment", "DaemonSet", "StatefulSet"})
		}
		icName := workload.CalculateWorkloadRuntimeObjectName(name, k)
		ic, err := kube.DefaultClient.OdigosClient.InstrumentationConfigs(ns).Get(ctx, icName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return withGuidance(map[string]any{
					"workload": fmt.Sprintf("%s/%s", kindStr, name), "namespace": ns, "runtime_detection": nil,
				}, "No InstrumentationConfig found — workload is not instrumented yet, or detection hasn't run.", []NextStep{
					{When: "to instrument this workload", Tool: "instrument_source",
						Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
				})
			}
			return mcp.NewToolResultError(fmt.Sprintf("get instrumentation config %q: %v", icName, err)), nil
		}

		type containerRuntime struct {
			ContainerName   string   `json:"containerName"`
			Language        string   `json:"language"`
			RuntimeVersion  string   `json:"runtimeVersion,omitempty"`
			CriErrorMessage string   `json:"criErrorMessage,omitempty"`
			OtherAgents     []string `json:"otherAgents,omitempty"`
		}
		details := make([]containerRuntime, 0, len(ic.Status.RuntimeDetailsByContainer))
		for _, d := range ic.Status.RuntimeDetailsByContainer {
			cr := containerRuntime{
				ContainerName:  d.ContainerName,
				Language:       string(d.Language),
				RuntimeVersion: d.RuntimeVersion,
			}
			if d.CriErrorMessage != nil {
				cr.CriErrorMessage = *d.CriErrorMessage
			}
			for _, agent := range d.OtherAgents {
				cr.OtherAgents = append(cr.OtherAgents, agent.Name)
			}
			details = append(details, cr)
		}
		ctxNote := fmt.Sprintf("Detected %d containers. If language is unknown, see describe_source's conditions for why (UnsupportedRuntimeVersion, OtherAgentDetected, etc.).", len(details))
		steps := []NextStep{
			{When: "to see full conditions and pod state", Tool: "describe_source",
				Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}},
			{When: "to override the detected runtime/distro on a specific container", Tool: "update_source",
				Args: map[string]any{"namespace": ns, "source_name": "<source name from list_sources>"}},
		}
		return withGuidance(map[string]any{
			"workload": fmt.Sprintf("%s/%s", kindStr, name), "namespace": ns, "runtime_detection": details,
		}, ctxNote, steps)
	}
}

// quiet a small subset of unused-import lint
var _ = k8sconsts.WorkloadKindDeployment

func getPodDetailsHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		details, err := services.GetPodDetails(ctx, ns, name)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("pod details: %v", err)), nil
		}
		return withGuidance(details, "Pod manifest + container statuses. Container restarts > 0 usually indicate an injection or runtime issue.", []NextStep{
			{When: "to restart the pod", Tool: "restart_pod", Args: map[string]any{"namespace": ns, "name": name}},
		})
	}
}

func listWorkloadsInNamespaceHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, err := req.RequireString("namespace")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := services.GetWorkloadsInNamespace(ctx, ns)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list workloads: %v", err)), nil
		}

		// include_runtime collapses the common "list workloads, then call
		// get_runtime_detection for each" fan-out (observed at ×17 in eval
		// trajectories) into ONE response: a single InstrumentationConfigs.List
		// supplies per-workload detected language/version, instead of N Gets.
		if boolArg(req, "include_runtime", false) {
			rtByIC := map[string][]map[string]string{}
			if ics, err := kube.DefaultClient.OdigosClient.InstrumentationConfigs(ns).List(ctx, metav1.ListOptions{}); err == nil {
				for i := range ics.Items {
					ic := &ics.Items[i]
					rows := make([]map[string]string, 0, len(ic.Status.RuntimeDetailsByContainer))
					for _, d := range ic.Status.RuntimeDetailsByContainer {
						rows = append(rows, map[string]string{
							"containerName":  d.ContainerName,
							"language":       string(d.Language),
							"runtimeVersion": d.RuntimeVersion,
						})
					}
					rtByIC[ic.Name] = rows
				}
			}
			type enriched struct {
				Namespace        string              `json:"namespace"`
				Name             string              `json:"name"`
				Kind             string              `json:"kind"`
				Instrumented     bool                `json:"instrumented"`
				RuntimeDetection []map[string]string `json:"runtimeDetection"`
			}
			rows := make([]enriched, 0, len(out))
			for _, w := range out {
				var rt []map[string]string
				instrumented := false
				if k := normalizeKind(string(w.Kind)); k != "" {
					if v, found := rtByIC[workload.CalculateWorkloadRuntimeObjectName(w.Name, k)]; found {
						rt, instrumented = v, true
					}
				}
				rows = append(rows, enriched{
					Namespace: ns, Name: w.Name, Kind: string(w.Kind),
					Instrumented: instrumented, RuntimeDetection: rt,
				})
			}
			return withGuidance(rows, fmt.Sprintf("%d workloads in %q with runtime detection inlined — no per-workload get_runtime_detection needed.", len(rows), ns),
				[]NextStep{
					{When: "to instrument one workload", Tool: "instrument_source"},
				})
		}

		return withGuidance(out, fmt.Sprintf("Found %d workloads in %q.", len(out), ns),
			[]NextStep{
				{When: "to instrument one workload", Tool: "instrument_source"},
				{When: "to instrument the whole namespace", Tool: "instrument_namespace", Args: map[string]any{"namespace": ns}},
				{When: "to also see detected language/version per workload in one call", Tool: "list_workloads_in_namespace", Args: map[string]any{"namespace": ns, "include_runtime": true}},
			})
	}
}
