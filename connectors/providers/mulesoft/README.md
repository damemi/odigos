# connector-mulesoft

The MuleSoft provider connector runtime. It implements the provider-agnostic `base.Provider` seam
(`connectors/base`) for the MuleSoft Anypoint Platform. It has one domain, **workloads**: Mule 4 applications.

| Type | Discovery | Instrumentation |
| --- | --- | --- |
| `mulesoft.cloudhub2-app` — CloudHub 2.0 shared spaces | yes | automatic and manual |
| `mulesoft.private-space-app` — CloudHub 2.0 private spaces | yes | built, declared `implemented: false` until verified |
| `mulesoft.rtf-app` — Runtime Fabric | yes | built, declared `implemented: false` until verified |
| `mulesoft.hybrid-app` — Runtime Manager-managed servers | yes | not per app (Direct Telemetry Stream is configured per server) |
| `mulesoft.omni-gateway` — Omni Gateway (managed and self-managed) | yes | managed: automatic and manual; self-managed: manual (unobservable) |

An application paired with an API Manager instance (autodiscovery, through an `api.id`-style property) carries the
instance on its spans as `mulesoft.api.*` resource attributes, in automatic mode; discovery reads API Manager once
per environment per cycle and keeps the last pairing when it cannot. An Omni Gateway is instrumented through
Gateway Manager by turning on its own OpenTelemetry tracing (`pkg/workload/instrumentor/gateway.go`); the
provider's JSON shape is unpublished, so every write is read back and undone if Anypoint did not keep it.

CloudHub 1.0 and Mule 3 are legacy and are not discovered. There are no compute platforms and no managed
collector.

Private spaces and Runtime Fabric use the same Application Manager write as CloudHub 2.0 and are registered in
the provider, but their instrumentation stays `implemented: false` in `spec/data/mulesoft.yaml` until it has been
verified against a real private space and fabric; removing the flag turns them on. Only a target known to be a
shared space is classified as `mulesoft.cloudhub2-app`, so an unidentified target is never instrumented.

Instrumentation turns on Mule's **Direct Telemetry Stream** — the runtime's own OTLP exporter — by setting
`mule.openTelemetry.*` application properties on the deployment. There is no agent, image or sidecar. It needs
Mule 4.11+ (metrics: 4.12+) and an Anypoint Advanced or Titanium subscription. The design, decisions and open
questions are in [`MULESOFT-CONNECTOR-PLAN.md`](../../MULESOFT-CONNECTOR-PLAN.md).

## Layout

```
cmd/main.go                         entry point: config -> base store -> base.Run(mulesoftprovider)
cmd/fake-anypoint                   serves the in-memory Anypoint API for local runs without an org
pkg/config                          env config (organization, Connected App, control plane) + live knobs
pkg/mulesoftprovider                base.Provider implementation
pkg/types                           spec type-name constants
pkg/workload/store                  connector_mulesoft.applications inventory
pkg/workload/detector               Application Manager scan (shared space, private space, RTF; one scan per cycle), hybrid
pkg/workload/instrumentor           desired properties, manifest, reconciler, manual plan, planner, observer
../../anypoint                      stdlib-only Anypoint REST client + fake (shared with testconnection)
```

## Configuration (stamped by connector-runtime)

- `ODIGOS_MULESOFT_ORG_ID` — the organization boundary (`spec.account.id`).
- `ODIGOS_BOOTSTRAP_REGION` — the control plane: `us` or `eu`.
- `ANYPOINT_CLIENT_ID`, `ANYPOINT_CLIENT_SECRET` — the Connected App (client credentials).
- `ODIGOS_MULESOFT_BASE_URL` — only when running the binary by hand against the fake API. Never stamped by the
  controller, and no spec field maps to it.
- Plus the generic base env (`ODIGOS_CONNECTOR_NAME`, `ODIGOS_DB_SCHEMA=connector_mulesoft`, ...).

Live operational knobs (agents-api `updateConfig`, no restart):

| Key | Default | Meaning |
| --- | --- | --- |
| `traceSampleRatio` | `1.0` | `mule.openTelemetry.tracer.exporter.sampler.arg`. Changing it redeploys every instrumented app. |
| `maxRedeploysPerCycle` | `2` | Applications one reconcile pass may change. Every change restarts the app. |

## How instrumentation behaves

- **Only writes on a difference.** A properties change makes the platform roll the application. The reconciler
  reads the live deployment, computes the properties it should carry (`instrumentor/properties.go`), and patches
  only when they differ. A converged application is never touched again.
- **Never while rolling.** A deployment that is not `APPLIED`, is `FAILED`, or is stopped is left for a later pass.
- **Never over customer config.** An application that already sets a Direct Telemetry Stream export property, or
  carries the customer's `ODIGOS_IAC` marker, is reported with a conflict and not changed.
- **Exact rollback.** The `ODIGOS_MANAGED` property records every managed property's prior value; disabling the
  source restores them verbatim. `mule.openTelemetry.exporter.resource.attributes` is appended to, not replaced.
- Enabling Direct Telemetry Stream turns off Anypoint Monitoring's Telemetry Exporter for the application.
- **Manual mode** (`instrumentation` off for a type):
  - The planner (`instrumentor/planner.go`) serves each application's instructions with its own values filled
    in: the destination's protocol and endpoints, sample ratio, environment, organization and target type.
  - The observer (`instrumentor/observe.go`) judges the applied properties as Conformant, Partial or Absent.
  - Both read the one property-set definition in `instrumentor/manual.go`.
- **Destinations:** OTLP gRPC and OTLP/HTTP. HTTP gets per-signal paths (`/v1/traces`, `/v1/logs`, `/v1/metrics`).
- **No agent images.** Direct Telemetry Stream is part of the Mule runtime, so unlike AWS, GCP and Azure there is
  no artifacts image to build, pin or stamp.

Workload identity: `serviceName` is the app name and `region` is the **environment name** — the same app in
Sandbox and Production is two workloads.

## Local development

Against the fake API (no MuleSoft org; discovery and instrumentation state transitions work, telemetry does
not flow), run the connector binary by hand:

```bash
go run ./connectors/providers/mulesoft/cmd/fake-anypoint -addr :8080
ODIGOS_MULESOFT_BASE_URL=http://localhost:8080 ANYPOINT_CLIENT_ID=fake-client-id \
  ANYPOINT_CLIENT_SECRET=fake-client-secret ODIGOS_MULESOFT_ORG_ID=0e1f5c6a-1111-4c2b-9d3e-000000000001 \
  ODIGOS_CONNECTOR_NAME=mulesoft-dev DATABASE_URL=postgres://... go run ./connectors/providers/mulesoft/cmd
```

Connectors created through Central cannot use the fake: no spec field selects the API host, because test
connection runs in Central's backend for any signed-in user and a user-supplied host would be an SSRF.

Against a real org, use the demo apps in `odigos-apps/mulesoft` (CloudHub 2.0, RTF and hybrid targets, with
`make instrument` for manual mode).

Tests:

```bash
GOWORK=off go test -race ./...                                    # unit tests against the fake API
MULESOFT_STORE_TEST_DSN=postgres://... GOWORK=off go test ./pkg/workload/store/   # SQL against a migrated DB
```

## Not verified against a live org yet

The Anypoint endpoint paths and response fields marked "unverified" in `connectors/anypoint` (token, `me`,
targets, AMC PATCH semantics, hybrid servers) and the Connected App scope names in the spec. Confirm them against
the Exchange API reference with a real org before release.
