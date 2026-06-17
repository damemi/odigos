package k8s

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"

	enterprisedistros "github.com/odigos-io/odigos-enterprise/distros"
	odigosdistros "github.com/odigos-io/odigos/distros"
)

// distros.go owns the distro / instrumentation-agent management tools. A "distro"
// (OTel distribution) is the specific instrumentation agent — eBPF or SDK — Odigos
// uses to instrument a container for a given language. Most languages have a
// DEFAULT distro; some are opt-in and must be selected per-workload:
//   - cplusplus-enterprise: dedicated C++ eBPF distro
//   - opentelemetry-ebpf-instrumentation (OBI): language-agnostic eBPF, any language
//
// Before these tools the only way to switch a distro was the generic update_source
// container override, and you had to already know the distro name. These tools make
// distro discovery + selection a first-class, validated MCP operation — which is how
// you turn on instrumentation for a language that reports "no agent available".

var (
	distroProviderOnce sync.Once
	distroProvider     *odigosdistros.Provider
	distroProviderErr  error
)

// getDistroProvider lazily builds the tier-aware (community + enterprise) provider
// once. The provider reads embedded distro definitions, so this is cheap and stable
// for the process lifetime.
func getDistroProvider() (*odigosdistros.Provider, error) {
	distroProviderOnce.Do(func() {
		distroProvider, distroProviderErr = enterprisedistros.NewEnterpriseProvider()
	})
	return distroProvider, distroProviderErr
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func registerDistroTools(s *server.MCPServer, deps Deps) {
	s.AddTool(
		mcp.NewTool("list_distros",
			readAnno(),
			mcp.WithDescription("List the OTel distributions (instrumentation agents) available in this Odigos tier. For each: the language it targets, whether it's eBPF, whether it needs a pod restart, and whether it's the DEFAULT for its language or OPT-IN. Use this to discover how to instrument a language that has no default agent (e.g. C++ → cplusplus-enterprise or the language-agnostic opentelemetry-ebpf-instrumentation / OBI), then apply it with set_distro."),
			mcp.WithString("language", mcp.Description("Optional filter: only distros targeting this language (e.g. 'cplusplus', 'go', 'java'). Wildcard distros (OBI) are always included.")),
		),
		listDistrosHandler(),
	)
	s.AddTool(
		mcp.NewTool("get_distro",
			readAnno(),
			mcp.WithDescription("Return the full spec of one OTel distribution by name: language, eBPF flag, restart requirement, required parameters, supported runtime environments/versions, and description."),
			mcp.WithString("distro_name", mcp.Required(), mcp.Description("Distro name from list_distros (e.g. 'opentelemetry-ebpf-instrumentation', 'cplusplus-enterprise', 'golang-enterprise').")),
		),
		getDistroHandler(),
	)
	s.AddTool(
		mcp.NewTool("set_distro",
			writeAnno(),
			mcp.WithDescription("Select a specific OTel distribution for one container of a workload — the production way to ENABLE instrumentation for a language with no default agent (e.g. C++ via 'cplusplus-enterprise' or 'opentelemetry-ebpf-instrumentation' / OBI), or to switch a container between distros. Validates the distro exists and matches the container's detected language (wildcard distros match any language). Creates a workload Source if one doesn't exist and sets the per-container distro override; other containers' overrides are preserved."),
			mcp.WithString("namespace", mcp.Required(), mcp.Description("Workload namespace.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Workload kind: Deployment, DaemonSet, or StatefulSet.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("Workload name.")),
			mcp.WithString("container_name", mcp.Required(), mcp.Description("Container within the workload (from get_runtime_detection / describe_source).")),
			mcp.WithString("distro_name", mcp.Required(), mcp.Description("Distro name to apply (from list_distros).")),
			mcp.WithBoolean("dry_run", mcp.Description(dryRunDesc)),
		),
		setDistroHandler(),
	)
}

func listDistrosHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		prov, err := getDistroProvider()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load distros: %v", err)), nil
		}
		langFilter := stringArg(req, "language", "")
		defaults := prov.GetDefaultDistroNames()
		isDefault := make(map[string]bool, len(defaults))
		for _, dn := range defaults {
			isDefault[dn] = true
		}
		type row struct {
			Name               string   `json:"name"`
			DisplayName        string   `json:"displayName"`
			Language           string   `json:"language"`
			IsEbpf             bool     `json:"isEbpf"`
			NoRestartRequired  bool     `json:"noRestartRequired"`
			DefaultForLanguage bool     `json:"defaultForLanguage"`
			RequireParameters  []string `json:"requireParameters,omitempty"`
			Description        string   `json:"description,omitempty"`
		}
		out := make([]row, 0)
		for _, d := range prov.GetAllDistros() {
			wildcard := common.IsProgrammingLanguageWildcard(d.Language)
			if langFilter != "" && string(d.Language) != langFilter && !wildcard {
				continue
			}
			out = append(out, row{
				Name: d.Name, DisplayName: d.DisplayName, Language: string(d.Language),
				IsEbpf:             d.IsEbpf,
				NoRestartRequired:  d.RuntimeAgent != nil && d.RuntimeAgent.NoRestartRequired,
				DefaultForLanguage: isDefault[d.Name],
				RequireParameters:  d.RequireParameters,
				Description:        firstLine(d.Description),
			})
		}
		defStr := make(map[string]string, len(defaults))
		for lang, dn := range defaults {
			defStr[string(lang)] = dn
		}
		return withGuidance(map[string]any{
			"distros":           out,
			"defaultByLanguage": defStr,
		}, fmt.Sprintf("%d distros available. Languages absent from defaultByLanguage have no default agent — instrument them by selecting an opt-in distro with set_distro (e.g. cplusplus → cplusplus-enterprise or opentelemetry-ebpf-instrumentation).", len(out)),
			[]NextStep{
				{When: "to see one distro's full spec", Tool: "get_distro"},
				{When: "to apply a distro to a workload container", Tool: "set_distro"},
			})
	}
}

func getDistroHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("distro_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		prov, err := getDistroProvider()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load distros: %v", err)), nil
		}
		d := prov.GetDistroByName(name)
		if d == nil {
			names := make([]string, 0)
			for _, x := range prov.GetAllDistros() {
				names = append(names, x.Name)
			}
			return errorWithOptions(fmt.Sprintf("unknown distro %q", name), names)
		}
		return withGuidance(d, "Full distro spec.", []NextStep{
			{When: "to apply it to a workload container", Tool: "set_distro"},
		})
	}
}

func setDistroHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ns, kindStr, name, err := requireSourceTriple(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		container, err := req.RequireString("container_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		distroName, err := req.RequireString("distro_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		dryRun := boolArg(req, "dry_run", true)
		k := normalizeKind(kindStr)
		if k == "" {
			return errorWithOptions(fmt.Sprintf("invalid kind %q", kindStr), []string{"Deployment", "DaemonSet", "StatefulSet"})
		}

		// validate distro exists
		prov, err := getDistroProvider()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("load distros: %v", err)), nil
		}
		d := prov.GetDistroByName(distroName)
		if d == nil {
			names := make([]string, 0)
			for _, x := range prov.GetAllDistros() {
				names = append(names, x.Name)
			}
			return errorWithOptions(fmt.Sprintf("unknown distro %q", distroName), names)
		}

		// validate language match unless the distro is language-agnostic (wildcard).
		if !common.IsProgrammingLanguageWildcard(d.Language) {
			icName := workload.CalculateWorkloadRuntimeObjectName(name, k)
			if ic, err := kube.DefaultClient.OdigosClient.InstrumentationConfigs(ns).Get(ctx, icName, metav1.GetOptions{}); err == nil {
				for _, c := range ic.Status.RuntimeDetailsByContainer {
					if c.ContainerName == container && c.Language != "" && c.Language != d.Language {
						return mcp.NewToolResultError(fmt.Sprintf("distro %q targets language %q but container %q was detected as %q — pick a matching distro (see list_distros) or a wildcard one like opentelemetry-ebpf-instrumentation", distroName, d.Language, container, c.Language)), nil
					}
				}
			}
		}

		if dryRun {
			return withGuidance(map[string]any{
				"action": "set_distro", "container": container, "distro": distroName,
				"workload": fmt.Sprintf("%s/%s", kindStr, name),
			}, fmt.Sprintf("Dry-run: would set container %q to distro %q.", container, distroName),
				[]NextStep{{When: "to apply", Tool: "set_distro",
					Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name, "container_name": container, "distro_name": distroName, "dry_run": false}}})
		}

		selector := labels.SelectorFromSet(labels.Set{
			k8sconsts.WorkloadNameLabel:      name,
			k8sconsts.WorkloadNamespaceLabel: ns,
			k8sconsts.WorkloadKindLabel:      string(k),
		})
		setOverride := func(spec *odigosv1.SourceSpec) {
			dn := distroName
			for i := range spec.ContainerOverrides {
				if spec.ContainerOverrides[i].ContainerName == container {
					spec.ContainerOverrides[i].OtelDistroName = &dn
					return
				}
			}
			spec.ContainerOverrides = append(spec.ContainerOverrides, odigosv1.ContainerOverride{ContainerName: container, OtelDistroName: &dn})
		}

		var done *mcp.CallToolResult
		applyErr := retry.RetryOnConflict(conflictBackoff, func() error {
			existing, err := kube.DefaultClient.OdigosClient.Sources(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
			if err != nil {
				return err
			}
			steps := []NextStep{{When: "to verify the agent enabled", Tool: "describe_source",
				Args: map[string]any{"namespace": ns, "kind": kindStr, "name": name}}}
			if len(existing.Items) > 0 {
				src := &existing.Items[0]
				setOverride(&src.Spec)
				updated, err := kube.DefaultClient.OdigosClient.Sources(ns).Update(ctx, src, metav1.UpdateOptions{})
				if err != nil {
					return err
				}
				done, _ = withGuidance(map[string]any{"action": "updated", "source_name": updated.Name, "container": container, "distro": distroName},
					fmt.Sprintf("Set container %q to distro %q. The instrumentor reconciles within seconds — check AgentEnabled via describe_source.", container, distroName), steps)
				return nil
			}
			newSrc := &odigosv1.Source{
				ObjectMeta: metav1.ObjectMeta{GenerateName: workload.CalculateWorkloadRuntimeObjectName(name, k), Namespace: ns},
				Spec: odigosv1.SourceSpec{
					Workload: k8sconsts.PodWorkload{Kind: k, Name: name, Namespace: ns},
				},
			}
			setOverride(&newSrc.Spec)
			created, err := kube.DefaultClient.OdigosClient.Sources(ns).Create(ctx, newSrc, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			done, _ = withGuidance(map[string]any{"action": "created", "source_name": created.Name, "container": container, "distro": distroName},
				fmt.Sprintf("Created Source and set container %q to distro %q.", container, distroName), steps)
			return nil
		})
		if applyErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("set distro: %v", applyErr)), nil
		}
		return done, nil
	}
}
