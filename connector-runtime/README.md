# connector-runtime

The Central-cluster Kubernetes controller for the Odigos cloud-connectors feature. It reconciles
`OdigosCloudConnector` resources into provider connector workloads and mirrors runtime-authoritative
state into the CRD status cache.

## Responsibilities

- Watch `OdigosCloudConnector` (group `odigos.io`, defined in the OSS `odigos/api` module).
- Create/update a connector `Deployment` (strategy `Recreate`) + `Service` per CR, mounting the
  credentials Secret and the Postgres DSN Secret, and stamping the runtime env
  (`ODIGOS_CONNECTOR_NAME`, `ODIGOS_PROVIDER`, `ODIGOS_DB_SCHEMA`, `ODIGOS_EXPECTED_SCHEMA_VERSION`,
  regions/account, AWS credentials).
- Compute status conditions (`RuntimeDeploymentReady`, `SchemaReady`, `RuntimeRegistered`,
  `Authenticated`, `DiscoveryReady`), a coarse `phase`, and the runtime cache, from the Deployment
  status plus a best-effort read of `connector_platform.runtime_heartbeat`.
- Manage the finalizer. Bounded provider-resource cleanup (`deletionPolicy = cleanup`) is a later phase;
  today owner-referenced Deployment/Service are garbage-collected on delete.

It does **not** own provider inventory, the computed solution, or any provider DB writes — those belong
to the connector runtime (`connector-aws`).

## Layout

```
cmd/main.go                                   # controller-runtime manager bootstrap
internal/controller/odigoscloudconnector_controller.go   # Reconcile + status
internal/resources/resources.go               # Deployment/Service builders, naming, labels
internal/heartbeat/reader.go                  # best-effort pgx read of runtime_heartbeat
```

## Configuration (env)

| Var | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | — | Postgres DSN for best-effort heartbeat reads (empty disables them) |
| `CONNECTOR_DB_SECRET_NAME` | `odigos-connector-postgres` | Secret holding the DSN handed to connector pods |
| `CONNECTOR_DB_SECRET_KEY` | `dsn` | Key within that Secret |
| `CONNECTOR_AWS_IMAGE` | — | Image stamped onto AWS connector Deployments (overridable per-CR via `spec.runtime.image`) |

Flags: `--leader-elect`, `--metrics-bind-address` (`:8080`), `--health-probe-bind-address` (`:8081`).

## Build

`make build-connector-runtime` from the repo root (builds inside Docker; requires `GITHUB_TOKEN` and a
sibling `odigos` checkout at `../odigos` for local-path replaces until `odigos/api` is published).
`go build ./...` and `go test ./...` work locally today.
