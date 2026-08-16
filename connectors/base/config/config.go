// Package config holds the provider-agnostic connector runtime configuration. A provider embeds this
// Config in its own and adds provider-specific fields. The connector-runtime controller stamps these
// variables onto the connector Deployment it creates.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Config is the generic configuration shared by every provider connector runtime.
type Config struct {
	// ConnectorName is the connector identity (row discriminator across all platform/provider tables) and
	// the human-facing platform name shown in Central.
	ConnectorName string
	// PlatformID is the STABLE, cluster-unique id used as the Central proxy/routing id (the CR UID injected
	// by the controller). It must be unique so two clusters that each name their connector "aws" do not
	// collide on one routing entry in Central (last-writer-wins would flip routing between them). Falls
	// back to ConnectorName when unset (local runs). Distinct from ConnectorName, which stays the display
	// name and the Postgres row key.
	PlatformID string
	// Provider is the provider key; informational.
	Provider string
	// Schema is the provider's Postgres schema used for sources/destinations.
	Schema string
	// DatabaseURL is the Postgres DSN.
	DatabaseURL string
	// Capabilities mirrors the CRD spec workloads/computePlatforms allow-lists (the customer's per-type
	// allow-list, split by domain). It selects, together with the provider spec, which detectors/reconcilers
	// the base runs. This is CRD/bootstrap config (a permission set by the platform admin), NOT the
	// DB-backed operational config.
	Capabilities spec.Capabilities

	// GraphQLAddr is the GraphQL + health listen address.
	GraphQLAddr string
	// CLIAddr is the odictl CLI WebSocket (/ws) listen address (odictl defaults to :23678).
	CLIAddr   string
	PodName   string
	RuntimeID string
	Version   string

	// CentralBackendURL is the central-backend base URL dialed for the reverse-WebSocket agents-api
	// connection (http(s):// or ws(s)://, host[:port]). Empty runs local-only (no Central).
	CentralBackendURL string
}

// FromEnv builds the generic Config from environment variables, validating the universally-required
// values (connector name, database URL). Providers call this, then read their own env vars.
func FromEnv() (Config, error) {
	c := Config{
		ConnectorName:     os.Getenv("ODIGOS_CONNECTOR_NAME"),
		Provider:          os.Getenv("ODIGOS_PROVIDER"),
		Schema:            os.Getenv("ODIGOS_DB_SCHEMA"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		GraphQLAddr:       GetenvDefault("ODIGOS_GQL_ADDR", ":8080"),
		CLIAddr:           GetenvDefault("ODIGOS_CLI_ADDR", ":23678"),
		PodName:           os.Getenv("POD_NAME"),
		Version:           GetenvDefault("ODIGOS_RUNTIME_VERSION", "dev"),
		CentralBackendURL: os.Getenv("ODIGOS_CENTRAL_BACKEND_URL"),
	}
	// Operational tunables (discovery/heartbeat/reconcile intervals, shutdown timeout, discovery tags) are
	// NOT read here — they are DB-backed and served live by config.Manager, tunable via the agents-api
	// config/updateConfig operations without a pod restart.

	if v := os.Getenv("ODIGOS_CAPABILITIES"); v != "" {
		if err := json.Unmarshal([]byte(v), &c.Capabilities); err != nil {
			return c, fmt.Errorf("parse ODIGOS_CAPABILITIES: %w", err)
		}
		// A contradictory allow-list is as unusable as malformed JSON, and failing here is the only place it can
		// be caught: nothing upstream (the CRD list, the GraphQL input, connector-runtime's marshal) enforces one
		// entry per type. Refusing to start is deliberate — the alternative is guessing, and the guess the
		// reconciler gate would make is that Odigos may mutate a workload the customer may have meant to own.
		if err := c.Capabilities.Validate(); err != nil {
			return c, fmt.Errorf("invalid ODIGOS_CAPABILITIES: %w", err)
		}
	}

	c.RuntimeID = c.PodName
	if c.RuntimeID == "" {
		c.RuntimeID = c.ConnectorName
	}

	c.PlatformID = os.Getenv("ODIGOS_CONNECTOR_ID")
	if c.PlatformID == "" {
		c.PlatformID = c.ConnectorName
	}

	if c.ConnectorName == "" {
		return c, fmt.Errorf("ODIGOS_CONNECTOR_NAME is required")
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}

// GetenvDefault returns the env var value or def when unset/empty.
func GetenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
