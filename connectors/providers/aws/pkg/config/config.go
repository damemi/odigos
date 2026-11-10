// Package config loads the AWS connector runtime configuration from the environment. It embeds the
// provider-agnostic base config and adds the AWS-specific fields (account, credentials). The
// connector-runtime controller stamps these variables onto the connector Deployment it creates.
package config

import (
	"encoding/json"
	"os"
	"strings"

	baseconfig "github.com/odigos-io/odigos/connectors/base/config"
	cpg "github.com/odigos-io/odigos/connectors/postgres"
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
	// ECSAgentImage is the container image for the Odigos ecs-agent (the vm-agent's eBPF daemon) the
	// connector deploys onto an EC2-backed ECS cluster (the ecs-cluster service). Sourced from
	// ODIGOS_ECS_AGENT_IMAGE; the actual deploy is a stub in this release, so it is informational until
	// the agent task spec is finalized. NOT the Fargate agents bundle — that is FargateAgentsImage.
	ECSAgentImage string
	// FargateAgentsImage is the agents-bundle image the ECS Fargate instrumentor's odigos-agent sidecar
	// stages the language agent files from (busybox + env-driven OpenTelemetry agents under
	// /instrumentations, built and released by odigos-io/aws-connector-artifacts). Sourced from
	// ODIGOS_FARGATE_AGENTS_IMAGE; empty uses DefaultFargateAgentsImage.
	FargateAgentsImage string
}

// DefaultFargateAgentsImage is the published multi-arch (amd64+arm64) Fargate agents bundle used when
// ODIGOS_FARGATE_AGENTS_IMAGE is not set. Fargate tasks must have image-pull egress to this registry.
//
// The tag MUST equal the version in connectors/providers/aws/ARTIFACTS_VERSION. This bundle and the
// Lambda layers come from ONE release of odigos-io/aws-connector-artifacts, and TestArtifactVersions
// enforces the match. Without it this is a Go constant and the Dockerfile's LAMBDA_AGENTS_IMAGE is a
// build arg, with nothing forcing them to agree — a connector could ship v0.3.0 Lambda layers while
// staging v0.1.0 Fargate agents, and nobody would notice until a customer's ECS task behaved unlike
// their Lambdas.
const DefaultFargateAgentsImage = "registry.odigos.io/odigos-fargate-agents:v0.0.27"

// ArtifactsVersion is the odigos-io/aws-connector-artifacts release this connector build is pinned to, as
// a Go value rather than only as a file the tests read.
//
// It exists because the published instrumentation instructions have to NAME it. The CDK artifacts tell a
// customer which @odigos/aws-cdk to install, and that package's version is this release's — so leaving
// <PACKAGE_VERSION> for the customer to guess would hand them the one blank in the document that Odigos
// actually knows the answer to. See CDKPackageVersion.
//
// TestArtifactVersions keeps this equal to the ARTIFACTS_VERSION file, alongside the two image pins.
const ArtifactsVersion = "v0.0.27"

// CDKPackageVersion is the @odigos/aws-cdk (and odigos-aws-cdk) version matching this connector build.
//
// The CDK packages are cut by the same release that publishes the layers and the agents bundle, and are
// versioned as semver without the tag's leading "v" — npm and PyPI both reject it. One conversion here
// rather than a second version constant, so there is nothing to keep in sync.
func CDKPackageVersion() string { return strings.TrimPrefix(ArtifactsVersion, "v") }

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

	if err := bc.ResolveBootstrapRegion("aws"); err != nil {
		return Config{}, err
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
		LambdaLayerARNs:    parseLayerARNs(os.Getenv("ODIGOS_AWS_LAMBDA_LAYER_ARNS")),
		LambdaLayerDir:     os.Getenv("ODIGOS_AWS_LAMBDA_LAYER_DIR"),
		ECSAgentImage:      os.Getenv("ODIGOS_ECS_AGENT_IMAGE"),
		FargateAgentsImage: envOrDefault("ODIGOS_FARGATE_AGENTS_IMAGE", DefaultFargateAgentsImage),
	}, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// KeyDebugLambdaTelemetry is the DB-backed config key toggling verbose in-function OpenTelemetry logging
// (OTEL_LOG_LEVEL=debug) on instrumented Lambdas. Default false; tunable live via the agents-api
// updateConfig operation (no pod restart).
const KeyDebugLambdaTelemetry = "debugLambdaTelemetry"

// KeyDebugEcsTelemetry is the DB-backed config key toggling verbose in-task OpenTelemetry logging
// (OTEL_LOG_LEVEL=debug) on instrumented Fargate services. A separate key from the Lambda toggle so
// debugging one workload kind never floods the other's logs.
const KeyDebugEcsTelemetry = "debugEcsTelemetry"

// Defaults returns the AWS-specific operational config defaults. They are merged onto the base defaults
// (config.MergeDefaults) when the connector builds its config.Manager, so every key resolves even when the
// connector has never been configured.
func Defaults() map[string]any {
	return map[string]any{
		KeyDebugLambdaTelemetry: false,
		KeyDebugEcsTelemetry:    false,
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
