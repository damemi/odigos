// Package types holds Azure connector type name constants used by the provider runtime.
// Values match connectors/spec/data/azure.yaml (features.*.types[].name).
package types

const (
	// ContainerApps is the directly-instrumentable workload type (Azure Container Apps).
	ContainerApps = "azure.container-apps"
	// AKS and VM are managed compute platforms Odigos is installed onto (not instrumented directly).
	AKS = "azure.aks"
	VM  = "azure.vm"
)
