package config

// Local development defaults
const (
	DevCentralBackendHostDefault = "localhost:8081"
	DevClusterNameDefault        = "local"
	DevVersionValue              = "v1.20.0"
	DevDeploymentIDDefault       = "dev-proxy-id"
)

var DevIgnoredNamespacesValue = []string{"kube-system", "kube-public", "kube-node-lease", "odigos-system"}

func DevCentralBackendURL() string   { return DevCentralBackendHostDefault }
func DevClusterName() string         { return DevClusterNameDefault }
func DevVersion() string             { return DevVersionValue }
func DevIgnoredNamespaces() []string { return DevIgnoredNamespacesValue }
func DevDeploymentID() string        { return DevDeploymentIDDefault }
