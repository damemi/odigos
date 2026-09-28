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

## The public OpAMP endpoint

A managed Collector runs in the customer's cloud account and connects **back** to Central to be
configured. The address it connects to is per environment — staging and production are different
Central installations — and travels as one value:

```text
cloudConnectors.managedCollectors.externalEndpoint   (odigos-central chart)
  -> CONNECTOR_OPAMP_ENDPOINT                        (connector-runtime)
  -> ODIGOS_OPAMP_ENDPOINT                           (each connector Deployment)
  -> the Collector's workload definition             (when the connector deploys one)
```

Validated once, in `connectors/opamp`, so the controller and the connector cannot hold different
opinions about the same string. It must be a URL the Collector's Supervisor accepts (`wss`, `ws`,
`https`, `http`) and, for any host that is not loopback, one of the encrypted two. Plaintext is refused
because the Collector presents a bearer credential on this connection and that credential can read the
destination's own credential out of a configuration push; loopback is allowed so a port-forwarded
gateway can be developed against. Credentials in the URL, a query string, and a fragment are all
refused — the first would be copied into every Collector's workload definition, and the other two are
typos that still connect, to the wrong place. A host with no path gets `/v1/opamp`, which is OpAMP's
default and what the gateway serves.

**There is no default.** No address is right for an unknown Central installation, and a wrong one would
point Collectors in a customer's cloud account at somebody else's endpoint. Unset means no Collector is
deployed — and a connector that was *allowed* to manage one says so on its own resource, as an
`OpAMPEndpointUnavailable` Event naming the chart value to set. Connectors with no managed-Collector
capability stay quiet: a warning on every connector in an installation that does not use the feature is
how an operator learns to ignore the one that matters.

### Changing the endpoint

Changing the chart value rewrites exactly one env var on each connector Deployment, so the connector
pods roll once and nothing else about them is disturbed (the immutable pod selector is untouched).

Collectors already deployed are a separate matter: each holds the endpoint injected into its workload
definition at deploy time, exactly as it holds its credential, so **moving Central does not move the
Collectors already out there**. Until each is redeployed its configuration plane is down — it keeps
exporting telemetry under its last configuration, and Odigos can no longer change it.

`collectorgateway.CheckEndpointDrift` reports that per Collector, and the two modes cure it
differently: in automatic mode Odigos replaces the Collector's tasks, and in manual mode the customer
redeploys. Re-spelling the same endpoint is not a change — comparison is on the normalized form,
because a cosmetic edit that read as a move would redeploy the whole fleet. Neither is a *broken* new
value: an endpoint that does not resolve reports as a configuration fault and changes nothing, so a
typo in the chart cannot redeploy anything.

### Verifying an environment

```bash
kubectl -n odigos-system get deploy connector-runtime \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CONNECTOR_OPAMP_ENDPOINT")].value}'

# every connector the controller generated
kubectl -n odigos-system get deploy -l odigos.io/component=connector-runtime \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.template.spec.containers[0].env[?(@.name=="ODIGOS_OPAMP_ENDPOINT")].value}{"\n"}{end}'

# what the controller refused, and why
kubectl -n odigos-system get events --field-selector reason=OpAMPEndpointUnavailable
```

Both environments are exercised the same way; the values differ, and staging's endpoint must never
appear on a production connector. The second command is the check for that — an empty column on a
connector that has the managed-Collector capability means no Collector will be deployed.

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
| `CONNECTOR_OPAMP_ENDPOINT` | — | Public endpoint managed Collectors connect back to, stamped as `ODIGOS_OPAMP_ENDPOINT`. See below; empty disables managed-Collector deployment |

Flags: `--leader-elect`, `--metrics-bind-address` (`:8080`), `--health-probe-bind-address` (`:8081`).

## Build

`make build-connector-runtime` from the repo root (builds inside Docker; requires `GITHUB_TOKEN` and a
sibling `odigos` checkout at `../odigos` for local-path replaces until `odigos/api` is published).
`go build ./...` and `go test ./...` work locally today.
