// Package postgres holds the cross-service Postgres contract for cloud connectors: the schema names,
// the version a runtime built against this module expects to find at startup, and the shape of the
// platform heartbeat/registration rows. Both the provider connector runtimes and (future) central
// readers import these constants so the wire contract never drifts from a stringly-typed copy.
package postgres

// Schema names owned by the connector migrations (one platform schema + one schema per provider).
const (
	SchemaPlatform = "connector_platform"
	SchemaAWS      = "connector_aws"
	SchemaGCP      = "connector_gcp"
	SchemaAzure    = "connector_azure"
)

// ExpectedSchemaVersions maps a schema name to the version a runtime built from this module requires
// to find in connector_platform.schema_versions at startup. Bump a value in lockstep with the
// migration that finalizes that schema. A runtime verifies these and fails fast on mismatch — it
// never runs migrations itself (see the cloud-connectors design, schema migrations).
var ExpectedSchemaVersions = map[string]string{
	SchemaPlatform: "2",
	SchemaAWS:      "2",
	SchemaGCP:      "1",
	SchemaAzure:    "1",
}

// AWSExpectedVersion is the connector_aws schema version the AWS runtime is built against. It is the
// value the connector-runtime controller stamps into ODIGOS_EXPECTED_SCHEMA_VERSION for AWS pods.
func AWSExpectedVersion() string { return ExpectedSchemaVersions[SchemaAWS] }

// GCPExpectedVersion is the connector_gcp schema version the GCP runtime is built against. It is the
// value the connector-runtime controller stamps into ODIGOS_EXPECTED_SCHEMA_VERSION for GCP pods.
func GCPExpectedVersion() string { return ExpectedSchemaVersions[SchemaGCP] }

// AzureExpectedVersion is the connector_azure schema version the Azure runtime is built against. It is
// the value the connector-runtime controller stamps into ODIGOS_EXPECTED_SCHEMA_VERSION for Azure pods.
func AzureExpectedVersion() string { return ExpectedSchemaVersions[SchemaAzure] }

// Platform tables.
const (
	TableSchemaVersions    = "connector_platform.schema_versions"
	TableRuntimeHeartbeat  = "connector_platform.runtime_heartbeat"
	TableInstallationAudit = "connector_platform.installation_audit"
)

// HeartbeatStatus values written into connector_platform.runtime_heartbeat.status. These mirror the
// coarse CloudConnectorPhase values on the CRD status.
const (
	HeartbeatStatusPending   = "Pending"
	HeartbeatStatusStarting  = "Starting"
	HeartbeatStatusConnected = "Connected"
	HeartbeatStatusDegraded  = "Degraded"
	HeartbeatStatusError     = "Error"
	HeartbeatStatusDisabled  = "Disabled"
	HeartbeatStatusDeleting  = "Deleting"
)
