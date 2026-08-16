# connectors

Shared library for the Odigos cloud-connectors feature (`platformType = connector`). It holds the
provider-agnostic and AWS-specific pieces that are imported by both the connector runtimes and
central-backend, so the wire/permission contracts never drift between them.

> **Full feature context (architecture, repo map, delivery timeline, Azure PR):**
> see [CLOUD-CONNECTORS.md](./CLOUD-CONNECTORS.md).

Everything is keyed by provider so new providers (gcp, azure, …) slot in alongside aws: a
`spec/data/<provider>.yaml` declaration, a `testconnection/<provider>.go`, and a `providers/<provider>/` runtime.

## Packages

- `postgres` — the cross-service Postgres contract: schema names (`connector_platform`,
  `connector_aws`), the `ExpectedSchemaVersions` map a runtime verifies at startup, the platform
  table names, and heartbeat status constants. Bump a version here in lockstep with the migration
  that finalizes a schema (see `connector-postgres/migrations`).
- `spec` — the **generic connector-definition interface**: the `CloudConnectorSpec` model
  (configurable fields, permission sets per feature **and per supported type**, each instrumentation
  type's generic injection `method` — `agent`/`sidecar`/`service`), a YAML `Parse`, helper methods
  (`ActivePermissions`/`ActiveTypes`/`PermissionsForType`/`MethodForType`), and a provider **registry**
  (`Register`/`Get`/`All`). Provider YAML files live under `spec/data/` (one file per provider,
  e.g. `aws.yaml`) and are embedded and registered when the package is imported — the same layout as
  odigos `destinations/data`. Provider runtimes that need typed constants for their implementation
  (e.g. AWS type name strings) define them alongside the runtime, not in this package.
- `base/domain/plan` — the provider-agnostic model of **what instrumentation a workload needs**, as ordered
  anchored fragments ("add THIS at THAT path"), used to publish instructions for manual (IaC) mode. Pure: no
  I/O, no provider SDK types. It owns the one thing that must not be re-derived per provider — how a value
  combines with one the customer already has (`set`/`append`/`prepend`/`remove` plus the separator), since a
  loader variable rendered as a literal silently destroys the customer's value and rendering it on the wrong
  side fails nothing visibly. Also owns `Prose` (the tool-agnostic rendering every provider can offer) and
  `CoverageProblems`, the guard that the hand-authored per-tool artifacts in `spec/data/<provider>.yaml` still
  say what the plans mean, and `Substitute` / `Unresolved`, which fill a workload's values into an artifact and
  report the blanks that are left with what belongs there. Each provider builds its plans from the same recipes
  its injector uses — see `DesiredPlan(s)` under `providers/<p>/pkg/workload/instrumentor`.

  **This model does not cross the wire.** `base/domain/instrumentationplan` renders it to text and that text is
  what the `instrumentationInstructions` query serves. Fragments, merge directions and helper containers stay
  internal, because a client that acted on them would be a second implementation of this rendering — free to
  disagree with the one that then reports the customer's deployment as drifted — and every new injection
  mechanic would cost an agents-api version for internals nothing acts on.
- `testconnection` — a provider-agnostic registry of read-only connection checks (identity,
  account match, region access, discovery permissions) run with **in-memory** credentials. Each
  provider registers a `Tester` (see `aws.go` / `gcp.go` / `azure.go`); callers dispatch with `Run(ctx, Input)` keyed by
  `Input.Provider`. Imported by central-backend so the create-connector flow can validate a connector
  before any Secret or connector pod exists. It must never persist, log, or echo credentials.

## Runtimes

- `providers/aws/` — the AWS connector runtime (its own Go module; `replace => ..` for this lib). See
  `providers/aws/README.md` if present.
- `providers/gcp/` — the GCP connector runtime (its own Go module; `replace => ..` for this lib). See
  `providers/gcp/README.md`.
- `providers/azure/` — the Azure connector runtime (its own Go module; `replace => ..` for this lib). See
  `providers/azure/README.md`.

## Editing a provider spec

Edit `spec/data/<provider>.yaml` and run the tests (`go test ./spec/...`). The spec is embedded, so no
regeneration step is needed — the change is picked up at build time.

The `instrumentation:<type>` sections are the exception to "just edit the YAML": they are the manual-mode
instructions a customer applies, and they are checked against the connector's own injection recipes by
`TestInstrumentationArtifactsMatchPlans` in each provider module. Run
`go test ./pkg/workload/instrumentor/...` from `providers/<provider>` after touching one. If it fails, the
recipe and the published instructions have diverged — fix the recipe, or update both; editing a value in the
YAML to make the test pass is how a customer ends up applying a previous release's configuration and being
told their deployment has drifted.

## Consumers

`connectors/providers/{aws,gcp,azure}`, `connector-runtime` and `central-backend` import this lib module
via a local `replace` until it is published alongside the enterprise release.
