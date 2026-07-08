// Package config loads the AWS connector runtime configuration from the environment. It embeds the
// provider-agnostic base config and adds the AWS-specific fields (account, credentials). The
// connector-runtime controller stamps these variables onto the connector Deployment it creates.
package config

import (
	"encoding/json"
	"os"

	baseconfig "github.com/odigos-io/odigos-enterprise/connectors/base/config"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

// Credentials holds the provider credentials the runtime uses for discovery. Either static keys or an
// assumable role ARN may be set; if neither is, the ambient AWS default credential chain (env / IRSA)
// is used.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	RoleARN         string
	ExternalID      string
}

// Config is the AWS connector configuration: the generic base config plus AWS-specific fields. The
// connector scans every region enabled in the account (resolved at runtime via ec2:DescribeRegions),
// so there is no regions field.
type Config struct {
	baseconfig.Config

	AccountID   string
	Credentials Credentials
	// LambdaLayerARNs is the public OpenTelemetry Lambda layer table consumed by the instrumentor,
	// keyed "<region>/<language>/<arch>" (looser fallbacks "<region>/<language>" and "<language>" are
	// also honored) → full layer ARN. Sourced from ODIGOS_AWS_LAMBDA_LAYER_ARNS (a JSON object); empty
	// when unset, in which case Lambda instrumentation fails per-function with a clear "configure layer
	// ARNs" error rather than attaching a wrong layer.
	LambdaLayerARNs map[string]string
	// LambdaLayerDir overrides where the connector image stages bundled OTel Lambda layer artifacts
	// (<dir>/<language>/<arch>/layer.zip). Empty uses the instrumentor default (/var/odigos/lambda).
	LambdaLayerDir string
	// ECSAgentImage is the container image for the Odigos ecs-agent the connector deploys onto an
	// EC2-backed ECS cluster (the ecs-cluster service). Sourced from ODIGOS_ECS_AGENT_IMAGE; the actual
	// deploy is a stub in this release, so it is informational until the agent task spec is finalized.
	ECSAgentImage string
}

// FromEnv builds the Config from environment variables (base requires connector name + database URL).
func FromEnv() (Config, error) {
	bc, err := baseconfig.FromEnv()
	if err != nil {
		return Config{}, err
	}
	// AWS defaults for the generic provider/schema fields.
	if bc.Provider == "" {
		bc.Provider = "aws"
	}
	if bc.Schema == "" {
		bc.Schema = cpg.SchemaAWS
	}

	return Config{
		Config:    bc,
		AccountID: os.Getenv("ODIGOS_AWS_ACCOUNT_ID"),
		Credentials: Credentials{
			AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
			SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
			RoleARN:         os.Getenv("ODIGOS_AWS_ROLE_ARN"),
			ExternalID:      os.Getenv("ODIGOS_AWS_EXTERNAL_ID"),
		},
		LambdaLayerARNs: parseLayerARNs(os.Getenv("ODIGOS_AWS_LAMBDA_LAYER_ARNS")),
		LambdaLayerDir:  os.Getenv("ODIGOS_AWS_LAMBDA_LAYER_DIR"),
		ECSAgentImage:   os.Getenv("ODIGOS_ECS_AGENT_IMAGE"),
	}, nil
}

// KeyDebugLambdaTelemetry is the DB-backed config key toggling verbose in-function OpenTelemetry logging
// (OTEL_LOG_LEVEL=debug) on instrumented Lambdas. Default false; tunable live via the agents-api
// updateConfig operation (no pod restart).
const KeyDebugLambdaTelemetry = "debugLambdaTelemetry"

// Defaults returns the AWS-specific operational config defaults. They are merged onto the base defaults
// (config.MergeDefaults) when the connector builds its config.Manager, so every key resolves even when the
// connector has never been configured.
func Defaults() map[string]any {
	return map[string]any{
		KeyDebugLambdaTelemetry: false,
	}
}

// parseLayerARNs decodes the JSON layer-ARN table; a missing or malformed value yields an empty table.
func parseLayerARNs(v string) map[string]string {
	if v == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(v), &m); err != nil {
		return nil
	}
	return m
}
