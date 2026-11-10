// Package types holds GCP connector type name constants used by the provider runtime.
// Values match connectors/spec/data/gcp.yaml (features.*.types[].name).
package types

const (
	// CloudRun is the directly-instrumentable workload type (Cloud Run services).
	CloudRun = "gcp.cloud-run"
	// GKE and GCE are managed compute platforms Odigos is installed onto (not instrumented directly).
	GKE = "gcp.gke"
	GCE = "gcp.gce"
)
