# connector-aws

The AWS provider connector runtime for the Odigos cloud-connectors feature. One instance owns one AWS
account boundary. It speaks the shared **agents-api** GraphQL contract (like the vm-agent).

## Architecture: workloads vs services

The connector splits discovery and reconciliation into two domains:

| Domain | Examples | Instrumentable? | Store | Reports |
| --- | --- | --- | --- | --- |
| **Workloads** | Lambda, Fargate tasks | Yes — via `createSource` + instrumentation loop | `pkg/workload/store` | `workloadReports` |
| **Compute platforms** | ECS-EC2 clusters, Fargate clusters (coverage) | No — agent install / coverage grouping | `pkg/service/store` | `serviceReports` |

The base coordinator runs **four independent loops**: workload discovery, service discovery, workload
reconcile (instrumentation, destination-gated), and service reconcile (agent deploy, not destination-gated).

## What it does

1. Loads config + credentials from the environment (stamped by `connector-runtime`).
2. Verifies `connector_platform.schema_versions` matches the versions it was built against and **exits
   on mismatch** — it never migrates.
3. Registers + heartbeats into `connector_platform.runtime_heartbeat`.
4. Discovers workloads (Lambda, Fargate tasks) and services (ECS clusters) across enabled regions.
5. Instruments workloads directly (Lambda implemented; Fargate sidecar stubbed).
6. Assigns `aws.ecs-cluster` services for agent deploy (stubbed reconciler; source plumbing wired).
7. Serves the agents-api GraphQL schema over HTTP (`/graphql`) for Central to query, plus `/healthz`.
8. Serves the same agents-api schema over the odictl CLI WebSocket (`/ws`) on port `23678`.

Workload kinds (`aws.lambda`, `aws.fargate-task`) are surfaced via `workloadReports` and `supportedSourceKinds`.
Service kinds (`aws.ecs-cluster`, `aws.fargate-cluster`, `aws.eks`, `aws.ec2`) are surfaced via `serviceReports`.
Only `aws.ecs-cluster` is assignable via `createSource` (enables ecs-agent deploy on the cluster).

## Layout

```
cmd/main.go                    # startup: schema check -> register -> loops -> serve GraphQL
pkg/awsprovider                # wires workload + service domains into base.Provider
pkg/config                     # env config
pkg/awsclient                  # shared AWS config cache
pkg/workload/
  store/                       # lambda_functions + ecs_tasks inventory
  detector/                    # Lambda + Fargate task discovery
  instrumentor/                # Lambda instrumentation (+ Fargate stub)
pkg/service/
  store/                       # ecs_clusters inventory
  detector/                    # ECS cluster discovery + classification
  reconciler/                  # aws.ecs-cluster agent deploy (stub) + aws.eks/aws.ec2 stubs
```

## Configuration (env)

| Var | Required | Purpose |
| --- | --- | --- |
| `ODIGOS_CONNECTOR_NAME` | yes | Connector identity (discriminator across rows) |
| `DATABASE_URL` | yes | Postgres DSN |
| `ODIGOS_AWS_REGIONS` | yes | Comma-separated regions to discover |
| `ODIGOS_AWS_ACCOUNT_ID` | no | Expected account id |
| `ODIGOS_DB_SCHEMA` | no (`connector_aws`) | Provider schema |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | no | Static credentials |
| `ODIGOS_AWS_ROLE_ARN` / `ODIGOS_AWS_EXTERNAL_ID` | no | AssumeRole (else IRSA / default chain) |
| `ODIGOS_DISCOVERY_INTERVAL` | no (`5m`) | Discovery cadence |
| `ODIGOS_GQL_ADDR` | no (`:8080`) | GraphQL/health listen address |
| `ODIGOS_CLI_ADDR` | no (`:23678`) | odictl CLI WebSocket (`/ws`) listen address |

## Build & run locally

`make build-connector-aws` from the repo root. `go build ./...` and `go test ./...` work locally.

Smoke test against the bundled Postgres image:

```bash
# start postgres + apply migrations (see connector-postgres/README.md), then:
DATABASE_URL="postgres://postgres:odigos@127.0.0.1:5432/odigos_central?sslmode=disable" \
ODIGOS_CONNECTOR_NAME=aws-dev ODIGOS_AWS_REGIONS=us-east-1 \
AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
go run ./cmd
# workload kinds: curl -s localhost:8080/graphql -d '{"query":"{ supportedSourceKinds }"}' ...
# service reports: curl -s localhost:8080/graphql -d '{"query":"{ serviceReports { name kind region } }"}' ...
```

## Inspecting with odictl

Port-forward the connector pod's CLI port and run odictl with its default port (`23678`):

```bash
make port-forward-connector-aws
cd ~/code/vm-agent/cmd/odictl && go run .
```

Sources, destinations, workload/service reports, discovery and health views are backed by the connector;
actions, rules, config and profiling are not implemented.
