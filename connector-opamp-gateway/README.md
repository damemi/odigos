# connector-opamp-gateway

The public OpAMP endpoint for every remotely managed Odigos Collector.

One endpoint serves the whole fleet across AWS, Azure and GCP:

```text
wss://<central-host>/v1/opamp
```

The same gateway receives Collector own metrics at `https://<central-host>/v1/metrics` as
OTLP/HTTP protobuf. The base connector offers this URL over OpAMP after verifying the Collector's
description. The edge must route both paths to this gateway and preserve `Authorization` on each.
The metrics route accepts POST only, checks a metrics-only credential derived from the Collector's
OpAMP credential, and cannot be used to open an OpAMP session. The gateway strips that credential
before forwarding the authenticated Collector identity to the connector.

Each connection is authenticated by the credential the Collector presents and reverse-proxied to the
connector that owns that Collector's cloud account. Design: [`collector/enterprise/managed/OPAMP_ROUTING.md`](../collector/enterprise/managed/OPAMP_ROUTING.md).
Credential lifecycle: [`connectors/MANAGED-COLLECTOR.md`](../connectors/MANAGED-COLLECTOR.md) §6.3–6.4.

## What it does on a handshake

1. Reads the bearer credential from `Authorization` and hashes it.
2. Resolves the hash in `connector_platform.collector_credentials` — one point lookup on a unique index.
3. Checks the credential is live (`active` or `superseded`) and unexpired.
4. Checks the connector its binding names still exists, is not being deleted, and owns the same
   provider and account.
5. Strips the credential and every `X-Odigos-*` header the client sent, asserts the authenticated
   identity in their place, and proxies to the connector's OpAMP port.

Routing comes from authenticated state only. A query parameter, a header, or the OpAMP Agent
description cannot choose a connector — the Agent description arrives after the upgrade and is
client-controlled, so it is a consistency check for the connector to make, never a routing input.

## What it does not do

- **Issue, rotate or revoke credentials.** Those belong to the connector's managed Collector API.
  After gateway authentication, the connector records `activated_at` when it accepts the OpAMP
  session; rotation cleanup uses that connection proof and a grace period.
- **Terminate OpAMP.** The connector owns desired configuration and runs the OpAMP server. The gateway
  is a byte pipe once the handshake is authenticated.
- **Hold a credential value.** It hashes what was presented and forwards an identity. Nothing
  downstream of the handshake has the value, and the `Authorization` header does not cross the second
  hop.

## Why refusals are opaque

Every refused handshake is one status code and an empty body. Distinguishing "unknown credential" from
"revoked" confirms to whoever is probing that a token was once real, and a Collector cannot act on the
difference — all of them mean it is not getting configuration. The reason goes to the log with the
credential fingerprint (eight hex characters of the hash) so an operator can find the Collector.

The one distinction that is made is between the two status codes:

| Status | Meaning | Examples |
| ------ | ------- | -------- |
| `401` | The credential is not acceptable. | absent, unknown, revoked, never installed, expired, bound to another provider or account |
| `503` | The credential is fine; there is nowhere to route it. | connector deleted or recreated under a new UID, connector terminating, credential store unreachable, watch not yet synced |

A `503` keeps the Supervisor retrying. Answering a store outage with `401` would tell an entire fleet
its credentials are invalid over a database blip.

## Revocation closes open sessions

An OpAMP session lives for days and keeps receiving configuration pushes, each carrying the
destination's own credential. Refusing only the *next* handshake would leave a compromised credential's
holder connected for as long as it avoided reconnecting.

So every replica re-authorizes its open sessions on a timer (`ODIGOS_OPAMP_REVALIDATE_INTERVAL`,
default 30s) using the same `collectorgateway.Authorize` the handshake uses, and closes the ones that
stopped being acceptable. The sweep costs one credential read per *credential*, not per connection — a
Collector scaled to N tasks holds N sessions on one credential, and that is normal, not a replay.

A store outage during a sweep leaves sessions open on purpose: closing them would disconnect the whole
fleet, and every one of them would fail to reconnect for the same reason.

## Configuration

| Variable | Default | Meaning |
| -------- | ------- | ------- |
| `DATABASE_URL` | *(required)* | The `odigos_central` DSN. The gateway reads the platform schema and verifies its version at startup, exactly as the connector runtimes do. |
| `ODIGOS_NAMESPACE` | *(cluster-wide)* | Scopes the connector and Service watches. Set it so RBAC can be a Role rather than a ClusterRole. |
| `ODIGOS_OPAMP_GATEWAY_LISTEN_ADDR` | `:8080` | Serves the OpAMP path and the probes. |
| `ODIGOS_OPAMP_PATH` | `/v1/opamp` | The path the edge forwards. |
| `ODIGOS_OPAMP_REVALIDATE_INTERVAL` | `30s` | How long a revoked credential can keep an already-open session. Cannot be zero or negative. |

`/healthz` is liveness. `/readyz` reports whether this replica can actually authenticate — watch
synced and credential store reachable — so a replica that can only answer `503` leaves the edge's
rotation instead of absorbing connections it will refuse.

## Connector integration

The gateway resolves a connector's OpAMP address from the **Service** labelled
`odigos.io/connector-id=<CR UID>` that exposes a port named `opamp` (or port 4320).
`connector-runtime` sets that label and port when Collector management is enabled, and the
connector runs the OpAMP server. If the upstream is absent, an authenticated Collector is answered
`503` with `upstream-unresolvable` in the log.

That is deliberate. Recomputing the connector's workload name here would be a copy of a rule that lives
in `connector-runtime`, correct until the day it was not — and the failure would be a Collector routed
at a name nothing answers on, with every component reporting healthy.

Automatic provisioning and the manual admin mutation write to
`connector_platform.collector_credentials`.

## Build and test

```bash
go -C connector-opamp-gateway test ./... -race
```

The image builds from the repository root through the gateway Dockerfile:

```bash
docker build -f connector-opamp-gateway/Dockerfile -t connector-opamp-gateway:dev .
```
