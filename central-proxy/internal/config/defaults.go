package config

// Local development defaults
const (
	DevCentralBackendHostDefault = "localhost:8081"
	DevClusterNameDefault        = "local"
	DevVersionValue              = "v1.9.0"
)

func DevCentralBackendURL() string { return DevCentralBackendHostDefault }
func DevClusterName() string       { return DevClusterNameDefault }
func DevVersion() string           { return DevVersionValue }
