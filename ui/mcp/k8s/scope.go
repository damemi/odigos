package k8s

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/odigos-io/odigos/api/k8sconsts"
)

// scope.go covers the (namespace, kind, name) workload addressing every
// CRD-shaped tool uses, plus the optional rule-level workload scoping.
// Kept in one file so all naming/normalization decisions live together.

// normalizeKind accepts "deployment"/"Deployment"/"DEPLOYMENT" etc. and maps
// to the canonical WorkloadKind constant. Unknown values pass through
// unchanged so callers can still surface them in errors.
func normalizeKind(s string) k8sconsts.WorkloadKind {
	switch strings.ToLower(s) {
	case "deployment":
		return k8sconsts.WorkloadKindDeployment
	case "daemonset":
		return k8sconsts.WorkloadKindDaemonSet
	case "statefulset":
		return k8sconsts.WorkloadKindStatefulSet
	case "cronjob":
		return k8sconsts.WorkloadKindCronJob
	case "namespace":
		return k8sconsts.WorkloadKindNamespace
	}
	return k8sconsts.WorkloadKind(s)
}

// requireSourceTriple parses the (namespace, kind, name) triple that nearly
// every workload-targeted tool exposes. Lifted into one helper so the
// parameter names and descriptions stay aligned across the catalog.
func requireSourceTriple(req mcp.CallToolRequest) (ns, kind, name string, err error) {
	if ns, err = req.RequireString("namespace"); err != nil {
		return "", "", "", err
	}
	if kind, err = req.RequireString("kind"); err != nil {
		return "", "", "", err
	}
	if name, err = req.RequireString("name"); err != nil {
		return "", "", "", err
	}
	return ns, kind, name, nil
}

// parseOptionalWorkloadScope reads the workload_* params used by
// instrumentation rules to scope to a single workload, or nil for cluster-wide.
// Validates that all three are present together — partial scope is an error.
func parseOptionalWorkloadScope(req mcp.CallToolRequest) (*k8sconsts.SourcesScopes, error) {
	wlNs := stringArg(req, "workload_namespace", "")
	wlKind := stringArg(req, "workload_kind", "")
	wlName := stringArg(req, "workload_name", "")
	if wlNs == "" && wlKind == "" && wlName == "" {
		return nil, nil
	}
	if wlNs == "" || wlKind == "" || wlName == "" {
		return nil, fmt.Errorf("to scope to a workload, all of workload_namespace, workload_kind, workload_name are required")
	}
	return &k8sconsts.SourcesScopes{
		Sources: []k8sconsts.PodWorkload{{
			Kind:      normalizeKind(wlKind),
			Name:      wlName,
			Namespace: wlNs,
		}},
	}, nil
}
