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

## Capabilities (`ODIGOS_CAPABILITIES`)

The customer's per-type allow-list is stamped onto the connector Deployment as `ODIGOS_CAPABILITIES`,
a JSON object with one key per domain — `workloads`, `computePlatforms`, `collectors` — matching
`spec.Capabilities` in `connectors/spec`. A domain with nothing enabled is **omitted**, not emitted
empty: the value is a container env var, so a key that appeared unconditionally would roll every
connector pod in the fleet on upgrade.

### Managed Collectors and the annotation fallback

`collectors` comes from `spec.collectors[]` on the CR. On a cluster whose `OdigosCloudConnector` CRD
predates that field, the same list may be given as an annotation instead:

```yaml
metadata:
  annotations:
    odigos.io/collectors-capability: '[{"type":"aws.fargate-collector","management":true}]'
```

An annotation needs no CRD change, which is the point — a cluster can enable a managed Collector
before the CRD shipped by the `odigos-central` chart is upgraded.

- **`spec.collectors[]` wins wherever it is set.** A shadowed annotation is reported as an
  `InvalidCollectorsCapability` Event rather than ignored quietly: it is exactly what somebody edits
  when they want the capability to change, and then wonders why nothing happened. Remove it.
- **The annotation is parsed strictly** — unknown keys, a missing `type`, or anything that is not a
  JSON array is refused. It is hand-written, so a typo there is a typo, and guessing at it would
  enable something other than what was asked for.
- **The annotation is temporary.** Delete it once every cluster is on a CRD carrying the field; a shim
  nothing needs is a second place for the allow-list to disagree with itself.

A list the connector could not honor — a duplicate type with conflicting flags, or `deployment`
without `management` — is **dropped in full** and reported as an Event. The connector is still
deployed, still instruments workloads, and simply has no Collector deployer, so nothing is created in
the customer's cloud account. The alternative is worse: `config.FromEnv` in the connector refuses to
start on a contradictory allow-list, which would take workload instrumentation down over a Collector
the customer may not have deployed yet.

### Version skew

Neither side needs to know the other's version.

| | Old controller | New controller |
| --- | --- | --- |
| **Old connector image** | no `collectors` key, no Collector | key present; the image has no `Collectors` field and parses with non-strict `encoding/json`, so it ignores the key and wires no deployer |
| **New connector image** | no `collectors` key, so no Collector is derived | the capability takes effect |

Downgrading the CRD prunes `spec.collectors[]`, which drops the capability and stops Odigos converging
the Collector. It does **not** delete the Collector — it keeps running on its last configuration until
the customer removes it.

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
| `CONNECTOR_GCP_IMAGE` | — | Image stamped onto GCP connector Deployments (overridable per-CR via `spec.runtime.image`) |
| `CONNECTOR_AZURE_IMAGE` | — | Image stamped onto Azure connector Deployments (overridable per-CR via `spec.runtime.image`) |
| `CONNECTOR_AWS_FARGATE_AGENTS_IMAGE` | — | Optional override of the odigos-fargate-agents bundle, stamped as `ODIGOS_FARGATE_AGENTS_IMAGE` onto AWS connector Deployments (empty keeps the connector's compiled-in default) |

Flags: `--leader-elect`, `--metrics-bind-address` (`:8080`), `--health-probe-bind-address` (`:8081`).

## Build

`make build-connector-runtime` from the repo root (builds inside Docker; requires `GITHUB_TOKEN` and a
sibling `odigos` checkout at `../odigos` for local-path replaces until `odigos/api` is published).
`go build ./...` and `go test ./...` work locally today.
