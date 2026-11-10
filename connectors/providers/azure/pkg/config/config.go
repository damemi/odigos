// Package config loads the Azure connector runtime configuration from the environment. It embeds the
// provider-agnostic base config and adds the Azure-specific fields (subscription, credentials). The
// connector-runtime controller stamps these variables onto the connector Deployment it creates.
package config

import (
	"os"

	baseconfig "github.com/odigos-io/odigos/connectors/base/config"
	cpg "github.com/odigos-io/odigos/connectors/postgres"
)

// Credentials holds the provider credentials the runtime uses for discovery. When TenantID / ClientID /
// ClientSecret are empty the ambient DefaultAzureCredential chain (Workload Identity / Managed Identity /
// Azure CLI) is used.
type Credentials struct {
	TenantID     string
	ClientID     string
	ClientSecret string
}

// DefaultAgentImage is the container image the Container Apps instrumentor stages OpenTelemetry agents
// from. Reuses the serverless-oriented cloud-run-agents image (env-driven agents under /instrumentations).
// Override with ODIGOS_CONTAINERAPPS_AGENT_IMAGE.
const DefaultAgentImage = "us-central1-docker.pkg.dev/odigos-cloud/components/cloud-run-agents:v0.1.0"

// Config is the Azure connector configuration: the generic base config plus Azure-specific fields. The
// connector scans every resource group in the subscription via list-by-subscription APIs, so there is
// no resource-group selection field.
type Config struct {
	baseconfig.Config

	// SubscriptionID is the Azure subscription this connector manages (the connector boundary). All API
	// calls are scoped to it.
	SubscriptionID string
	Credentials    Credentials

	// AgentImage is the odigos-agents image the Container Apps instrumentor stages language agents from.
	AgentImage string
}

// FromEnv builds the Config from environment variables (base requires connector name + database URL).
func FromEnv() (Config, error) {
	bc, err := baseconfig.FromEnv()
	if err != nil {
		return Config{}, err
	}
	// Azure defaults for the generic provider/schema fields.
	if bc.Provider == "" {
		bc.Provider = "azure"
	}
	if bc.Schema == "" {
		bc.Schema = cpg.SchemaAzure
	}

	agentImage := os.Getenv("ODIGOS_CONTAINERAPPS_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = DefaultAgentImage
	}

	if err := bc.ResolveBootstrapRegion("azure"); err != nil {
		return Config{}, err
	}

	return Config{
		Config:         bc,
		SubscriptionID: os.Getenv("ODIGOS_AZURE_SUBSCRIPTION_ID"),
		Credentials: Credentials{
			TenantID:     os.Getenv("AZURE_TENANT_ID"),
			ClientID:     os.Getenv("AZURE_CLIENT_ID"),
			ClientSecret: os.Getenv("AZURE_CLIENT_SECRET"),
		},
		AgentImage: agentImage,
	}, nil
}

// KeyDebugTelemetry is the DB-backed config key toggling verbose OpenTelemetry logging
// (OTEL_LOG_LEVEL=debug) on instrumented Container Apps once instrumentation lands. Default false.
const KeyDebugTelemetry = "debugTelemetry"

// Defaults returns the Azure-specific operational config defaults. They are merged onto the base
// defaults (config.MergeDefaults) when the connector builds its config.Manager.
func Defaults() map[string]any {
	return map[string]any{
		KeyDebugTelemetry: false,
	}
}
