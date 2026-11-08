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

### Managed Collectors

Declare each Collector in `spec.collectors[]`, alongside `spec.workloads[]` and
`spec.computePlatforms[]`:

```yaml
spec:
  collectors:
    - type: aws.fargate-collector
      management: true
      deployment: true
```

Set `deployment: false` for a Collector installed by the customer. The connector
will discover and configure it when `management: true`.

- **`spec.collectors[]` wins wherever it is set.** A legacy annotation is reported as an
  `InvalidCollectorsCapability` Event rather than ignored quietly: it is exactly what somebody edits
  when they want the capability to change, and then wonders why nothing happened. Remove it.
- The legacy `odigos.io/collectors-capability` annotation remains readable for clusters
  with an older CRD. New connectors should use the spec field.

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

## Exposing the connector's OpAMP server

A connector that manages a Collector runs an OpAMP server on `4320` (`connectors/base/opamp`), and the
Collectors reach it through `connector-opamp-gateway`. Three things are generated for that, and **all
three follow the capability** — the same condition `base.Run` starts the server on:

- an `opamp` container port and an `opamp` Service port on `4320`;
- `odigos.io/connector-id=<CR UID>` on the Deployment, its pod template and the Service — how the
  gateway finds this connector's Service. By label, never by recomputing the workload name: that rule
  lives here, and a copy of it in the gateway would be correct until the day it was not, with the failure
  being a Collector routed at a name nothing answers on while every component reports healthy;
- a NetworkPolicy admitting only gateway pods to `4320`.

A connector with no Collector capability is generated exactly as it was before the feature existed — no
extra port, no pod roll from a new template label, and no NetworkPolicy on a pod that never had one. The
capability is read from `spec.collectors[]` or the compatibility annotation, so the plumbing appears
identically for either; a list that was refused produces none of it.

### The NetworkPolicy re-permits more than it restricts

The OpAMP port carries **no authentication of its own**. The gateway authenticated the Collector's
credential and the identity it asserts on `X-Odigos-*` headers is what the connector acts on — so the
NetworkPolicy is what stands between that identity and anything on the pod network that could forge one.

The non-obvious part is everything the policy has to allow back. A pod with no NetworkPolicy accepts
everything; the moment one selects it, **every port not explicitly allowed is denied**. A policy
mentioning only `4320` would take out the connector's GraphQL endpoint, its `odictl` endpoint and — on
CNIs that police node-to-pod traffic — its own readiness and liveness probes, leaving the connector
NotReady for a feature unrelated to either. So the policy allows `8080` and `23678` from anywhere, which
is precisely what they already were: it narrows one port and changes nothing else.

It is ingress-only. The connector dials out to Central, to Postgres and to the cloud provider, and an
egress policy here would be a second thing to maintain and the first thing to break all of that.

The gateway peer is selected by `odigos.io/component=connector-opamp-gateway`, with a namespace selector
added **only** when `CONNECTOR_OPAMP_GATEWAY_NAMESPACE` names a different namespace: an empty
`namespaceSelector` matches *every* namespace, which would admit any pod carrying the gateway's labels
from anywhere in the cluster.

The policy is owned by the CR and reconciled back if edited, and deleted when the connector stops
managing a Collector — a policy describing a port nothing serves is a thing to explain the next time
somebody debugs the connector's networking.

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
| `CONNECTOR_MULESOFT_IMAGE` | — | Image stamped onto MuleSoft connector Deployments (overridable per-CR via `spec.runtime.image`). MuleSoft has no agent-artifact image: Direct Telemetry Stream is built into the Mule runtime |
| `CONNECTOR_AWS_FARGATE_AGENTS_IMAGE` | — | Optional override of the odigos-fargate-agents bundle, stamped as `ODIGOS_FARGATE_AGENTS_IMAGE` onto AWS connector Deployments (empty keeps the connector's compiled-in default) |
| `CONNECTOR_AWS_AGENTCORE_AGENTS_IMAGE` | — | Optional override of the AgentCore SDK carrier named in manual-mode instructions (the connector never pulls it), stamped as `ODIGOS_AGENTCORE_AGENTS_IMAGE` onto AWS connector Deployments |
| `CONNECTOR_OPAMP_ENDPOINT` | — | Public endpoint managed Collectors connect back to, stamped as `ODIGOS_OPAMP_ENDPOINT`. See below; empty disables managed-Collector deployment |
| `CONNECTOR_COLLECTOR_IMAGE` | — | Installation-selected managed Collector image, stamped as `ODIGOS_COLLECTOR_IMAGE`. Must include a tag or digest. No compiled-in fallback. |
| `CONNECTOR_OPAMP_GATEWAY_NAMESPACE` | *(the connector's own)* | Where `connector-opamp-gateway` pods run, for the NetworkPolicy that admits them. Set only when the gateway runs in another namespace |

Flags: `--leader-elect`, `--metrics-bind-address` (`:8080`), `--health-probe-bind-address` (`:8081`).

The Central chart deploys this controller as one replica with a `Recreate` strategy. Leader election
is disabled in that deployment, so a rolling update could temporarily run two controllers with
different installation defaults. Both would apply the same connector Deployment, repeatedly
replacing its pods and racing Collector image selection. Keep the controller rollout non-overlapping;
one replica alone does not prevent rolling-update surge. Provider connector Deployments independently
use `Recreate` because their cloud reconciliation is also single-writer.

When upgrading an existing installation with server-side apply, Kubernetes may retain the defaulted
`rollingUpdate` field from the old Deployment and reject the strategy change. Before upgrading that
installation, migrate the strategy atomically (this does not change the pod template):

```sh
kubectl -n odigos-central patch deployment connector-runtime --type=merge \
  --patch '{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null}}}'
```

### Managed Collector image selection

The central Helm chart supplies the image through `cloudConnectors.managedCollectors.collectorImage`.
Its registry follows `imagePrefix` (normally `registry.odigos.io`) and its tag follows `image.tag` or
the chart's AppVersion. Override `registry`, `repository`, `tag`, or `digest` independently; a digest
wins over a tag. Set `reference` to supply the complete reference instead. This is separate from
`cloudConnectors.managedCollectors.image`, which selects the OpAMP gateway image.

Without reinstalling, update the existing connector config API at `collector.defaults.image` for all
regions or `collector.regions.<region>.image` for one region. Both accept `reference`, `registry`,
`repository` (a complete repository without a tag/digest), `tag`, and `digest`. The legacy
`repositoryOverride` alias remains supported. For example:

```yaml
collector:
  defaults:
    image:
      registry: mirror.example.com
      tag: v1.2.3
  regions:
    us-east-1:
      image:
        reference: mirror.example.com/custom/collector:v1.2.4
```

Runtime settings override the installation selection; regional settings override defaults. Setting
a tag clears an inherited digest, while an explicit digest takes precedence over that tag. A regional
`reference` replaces inherited image overrides. Clear runtime overrides to return to the installation
selection. Runtime updates are persisted and picked up on reconciliation. Automatic mode registers a
new task revision and rolls the Collector; manual/IaC mode reports image drift and renders updated
instructions, without changing customer resources. Tag-only selections detect tag changes, not a
registry republishing different bytes under the same tag; use a digest for immutable deployments.

For ECR Public, set `reference` to `public.ecr.aws/<alias>/<repository>:<tag>` (or `@sha256:<digest>`).
The ECS definition omits registry credentials for that hostname and pulls anonymously. The Odigos
license and OpAMP secrets are still required at runtime. Collector subnets need outbound HTTPS to
ECR Public; a private ECR VPC endpoint alone does not provide that internet access.

## Build

`make build-connector-runtime` from the repo root (builds inside Docker; requires `GITHUB_TOKEN` and a
sibling `odigos` checkout at `../odigos` for local-path replaces until `odigos/api` is published).
`go build ./...` and `go test ./...` work locally today.
