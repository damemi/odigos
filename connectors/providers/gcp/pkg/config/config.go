// Package config loads the GCP connector runtime configuration from the environment. It embeds the
// provider-agnostic base config and adds the GCP-specific fields (project, credentials). The
// connector-runtime controller stamps these variables onto the connector Deployment it creates.
package config

import (
	"os"

	baseconfig "github.com/odigos-io/odigos-enterprise/connectors/base/config"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

// Credentials holds the provider credentials the runtime uses for discovery. When CredentialsJSON is
// empty the ambient Application Default Credentials chain (GKE Workload Identity / metadata server /
// GOOGLE_APPLICATION_CREDENTIALS) is used.
type Credentials struct {
	// CredentialsJSON is the raw service account key JSON (from ODIGOS_GCP_CREDENTIALS_JSON). Empty uses
	// Application Default Credentials.
	CredentialsJSON string
}

// DefaultAgentImage is the container image the Cloud Run instrumentor stages the OpenTelemetry agents
// from. It is the dedicated, serverless-oriented Cloud Run agents image (built from
// connectors/providers/gcp/agents/Dockerfile), holding only PURELY ENV-DRIVEN agents under
// /instrumentations and providing sh/cp/nc for the copier sidecar. Unlike the odiglet odigos-agents image
// it does not ship the OpAMP-driven distros (e.g. nodejs-community), which cannot run on Cloud Run where
// there is no odiglet/OpAMP server; Node uses the upstream @opentelemetry/auto-instrumentations-node.
//
// It must live in a registry Cloud Run can pull from — only [region.]gcr.io, [region-]docker.pkg.dev
// (Artifact Registry) and docker.io are allowed. Override with ODIGOS_CLOUDRUN_AGENT_IMAGE (e.g. a mirror
// in the project's own Artifact Registry).
const DefaultAgentImage = "us-central1-docker.pkg.dev/odigos-cloud/components/cloud-run-agents:v0.1.0"

// Config is the GCP connector configuration: the generic base config plus GCP-specific fields. The
// connector scans every location/zone in the project via location wildcards, so there is no location
// selection field.
type Config struct {
	baseconfig.Config

	// ProjectID is the GCP project this connector manages (the connector boundary). All API calls are
	// scoped to it.
	ProjectID   string
	Credentials Credentials

	// AgentImage is the odigos-agents image the Cloud Run instrumentor stages the language agents from
	// (see DefaultAgentImage). Sourced from ODIGOS_CLOUDRUN_AGENT_IMAGE; empty falls back to the default.
	AgentImage string
}

// FromEnv builds the Config from environment variables (base requires connector name + database URL).
func FromEnv() (Config, error) {
	bc, err := baseconfig.FromEnv()
	if err != nil {
		return Config{}, err
	}
	// GCP defaults for the generic provider/schema fields.
	if bc.Provider == "" {
		bc.Provider = "gcp"
	}
	if bc.Schema == "" {
		bc.Schema = cpg.SchemaGCP
	}

	agentImage := os.Getenv("ODIGOS_CLOUDRUN_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = DefaultAgentImage
	}

	if err := bc.ResolveBootstrapRegion("gcp"); err != nil {
		return Config{}, err
	}

	return Config{
		Config:    bc,
		ProjectID: os.Getenv("ODIGOS_GCP_PROJECT_ID"),
		Credentials: Credentials{
			CredentialsJSON: os.Getenv("ODIGOS_GCP_CREDENTIALS_JSON"),
		},
		AgentImage: agentImage,
	}, nil
}

// KeyDebugTelemetry is the DB-backed config key toggling verbose OpenTelemetry logging
// (OTEL_LOG_LEVEL=debug) on instrumented Cloud Run services. Default false; tunable live via the
// agents-api updateConfig operation (no pod restart).
const KeyDebugTelemetry = "debugTelemetry"

// Defaults returns the GCP-specific operational config defaults. They are merged onto the base defaults
// (config.MergeDefaults) when the connector builds its config.Manager, so every key resolves even when
// the connector has never been configured.
func Defaults() map[string]any {
	return map[string]any{
		KeyDebugTelemetry: false,
	}
}
