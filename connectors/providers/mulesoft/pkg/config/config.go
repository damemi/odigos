// Package config loads the MuleSoft connector runtime configuration from the environment. It embeds the
// provider-agnostic base config and adds the Anypoint-specific fields (organization, Connected App credentials,
// control plane). The connector-runtime controller stamps these variables onto the connector Deployment.
package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/odigos-io/odigos/connectors/anypoint"
	baseconfig "github.com/odigos-io/odigos/connectors/base/config"
	cpg "github.com/odigos-io/odigos/connectors/postgres"
)

// Config is the MuleSoft connector configuration: the generic base config plus Anypoint-specific fields.
type Config struct {
	baseconfig.Config

	// OrganizationID is the Anypoint organization (business group) this connector manages — the boundary.
	OrganizationID string
	// ClientID and ClientSecret are the Connected App credentials.
	ClientID     string
	ClientSecret string
	// BaseURL is the Anypoint API root, resolved from the control plane (the bootstrap region). The
	// ODIGOS_MULESOFT_BASE_URL override exists only for running this binary by hand against the fake API: the
	// controller never stamps it and no spec field maps to it, so an in-cluster connector always talks to
	// MuleSoft.
	BaseURL string
}

// FromEnv builds the Config from environment variables.
func FromEnv() (Config, error) {
	bc, err := baseconfig.FromEnv()
	if err != nil {
		return Config{}, err
	}
	if bc.Provider == "" {
		bc.Provider = "mulesoft"
	}
	if bc.Schema == "" {
		bc.Schema = cpg.SchemaMuleSoft
	}
	if err := bc.ResolveBootstrapRegion("mulesoft"); err != nil {
		return Config{}, err
	}
	baseURL, err := anypoint.BaseURLFor(bc.BootstrapRegion, os.Getenv("ODIGOS_MULESOFT_BASE_URL"))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Config:         bc,
		OrganizationID: os.Getenv("ODIGOS_MULESOFT_ORG_ID"),
		ClientID:       os.Getenv("ANYPOINT_CLIENT_ID"),
		ClientSecret:   os.Getenv("ANYPOINT_CLIENT_SECRET"),
		BaseURL:        baseURL,
	}
	if cfg.OrganizationID == "" {
		return Config{}, fmt.Errorf("no Anypoint organization id configured (ODIGOS_MULESOFT_ORG_ID)")
	}
	return cfg, nil
}

// Operational config keys, DB-backed and tunable live through the agents-api updateConfig operation.
const (
	// KeyTraceSampleRatio is the head-sampling ratio written to mule.openTelemetry.tracer.exporter.sampler.arg.
	// It defaults to 1.0 so every span reaches the collector, where Odigos samples; lowering it is a property
	// change, so every instrumented application redeploys to pick it up (within the per-cycle redeploy cap).
	KeyTraceSampleRatio = "traceSampleRatio"
	// KeyMaxRedeploysPerCycle bounds how many applications one reconcile pass may change. Every properties
	// change restarts the application, so this is the blast radius of a bad configuration.
	KeyMaxRedeploysPerCycle = "maxRedeploysPerCycle"
)

// Default values for the operational keys.
const (
	DefaultTraceSampleRatio     = 1.0
	DefaultMaxRedeploysPerCycle = 2
)

// Defaults returns the MuleSoft-specific operational config defaults, merged onto the base defaults.
func Defaults() map[string]any {
	return map[string]any{
		KeyTraceSampleRatio:     DefaultTraceSampleRatio,
		KeyMaxRedeploysPerCycle: DefaultMaxRedeploysPerCycle,
	}
}

// Float reads a numeric value from an effective config map. Values arrive as JSON numbers (float64) from the
// database, or as strings when set by hand; anything else, or a value outside [min, max], yields def.
func Float(eff map[string]any, key string, def, min, max float64) float64 {
	var v float64
	switch x := eff[key].(type) {
	case float64:
		v = x
	case int:
		v = float64(x)
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return def
		}
		v = f
	default:
		return def
	}
	if v < min || v > max {
		return def
	}
	return v
}
