# connectors

Shared library for the Odigos cloud-connectors feature (`platformType = connector`). It holds the
provider-agnostic and AWS-specific pieces that are imported by both the connector runtimes and
central-backend, so the wire/permission contracts never drift between them.

Everything is keyed by provider so new providers (gcp, azure, …) slot in alongside aws: a
`spec/data/<provider>.yaml` declaration, a `testconnection/<provider>.go`, and a `providers/<provider>/` runtime.

## Packages

- **`postgres`** — the cross-service Postgres contract: schema names (`connector_platform`,
  `connector_aws`), the `ExpectedSchemaVersions` map a runtime verifies at startup, the platform
  table names, and heartbeat status constants. Bump a version here in lockstep with the migration
  that finalizes a schema (see `connector-postgres/migrations`).
- **`spec`** — the **generic connector-definition interface**: the `CloudConnectorSpec` model
  (configurable fields, permission sets per feature **and per supported type**, each instrumentation
  type's generic injection `method` — `agent`/`sidecar`/`service`), a YAML `Parse`, helper methods
  (`ActivePermissions`/`ActiveTypes`/`PermissionsForType`/`MethodForType`), and a provider **registry**
  (`Register`/`Get`/`All`). Provider YAML files live under **`spec/data/`** (one file per provider,
  e.g. `aws.yaml`) and are embedded and registered when the package is imported — the same layout as
  odigos `destinations/data`. Provider runtimes that need typed constants for their implementation
  (e.g. AWS type name strings) define them alongside the runtime, not in this package.
- **`testconnection`** — a provider-agnostic registry of read-only connection checks (identity,
  account match, region access, discovery permissions) run with **in-memory** credentials. Each
  provider registers a `Tester` (see `aws.go`); callers dispatch with `Run(ctx, Input)` keyed by
  `Input.Provider`. Imported by central-backend so the create-connector flow can validate a connector
  before any Secret or connector pod exists. It must never persist, log, or echo credentials.

## Runtimes

- **`providers/aws/`** — the AWS connector runtime (its own Go module; `replace => ..` for this lib). See
  `providers/aws/README.md` if present.

## Editing a provider spec

Edit `spec/data/<provider>.yaml` and run the tests (`go test ./spec/...`). The spec is embedded, so no
regeneration step is needed — the change is picked up at build time.

## Consumers

`connectors/providers/aws`, `connector-runtime` and `central-backend` import this lib module via a local
`replace` until it is published alongside the enterprise release.
