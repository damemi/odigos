package config

// Local development defaults
const (
	DevCentralBackendHostDefault = "localhost:8081"
	DevClusterNameDefault        = "local"
	DevVersionValue              = "v1.20.0"
	DevDeploymentIDDefault       = "dev-proxy-id"
)

func DevCentralBackendURL() string { return DevCentralBackendHostDefault }
func DevClusterName() string       { return DevClusterNameDefault }
func DevVersion() string           { return DevVersionValue }
func DevDeploymentID() string      { return DevDeploymentIDDefault }
