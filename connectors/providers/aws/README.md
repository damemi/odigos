# connector-aws

The AWS provider connector runtime for the Odigos cloud-connectors feature. One instance owns one AWS
account boundary. It speaks the shared **agents-api** GraphQL contract (like the vm-agent).

## Architecture: workloads vs compute platforms

The connector splits discovery and reconciliation into two domains:

| Domain | Examples | Instrumentable? | Store | Reports |
| --- | --- | --- | --- | --- |
| **Workloads** | Lambda, Fargate services/tasks | Yes — via `createSource` + instrumentation loop | `pkg/workload/store` | `workloadReports` |
| **Compute platforms** | ECS-EC2 clusters, Fargate clusters (coverage), EKS, EC2 | No — agent install / coverage grouping | `pkg/computeplatform/store` | `computePlatformReports` |

The base coordinator runs **four independent loops**: workload discovery, compute-platform discovery,
workload reconcile (instrumentation, destination-gated), and compute-platform reconcile (agent deploy,
not destination-gated).

## What it does

1. Loads config + credentials from the environment (stamped by `connector-runtime`).
2. Verifies `connector_platform.schema_versions` matches the versions it was built against and **exits
   on mismatch** — it never migrates.
3. Registers + heartbeats into `connector_platform.runtime_heartbeat`.
4. Discovers workloads (Lambda functions; Fargate ECS services + tasks) and compute platforms (ECS
   clusters, …) across enabled regions.
5. Instruments workloads directly: **Lambda** via an OTel layer + env on the function; **Fargate
   services** via an agent-staging sidecar injected into the service's task definition (see below).
6. Assigns `aws.ecs-cluster` compute platforms for agent deploy (stubbed reconciler; source plumbing wired).
7. Serves the agents-api GraphQL schema over HTTP (`/graphql`) for Central to query, plus `/healthz`.
8. Serves the same agents-api schema over the odictl CLI WebSocket (`/ws`) on port `23678`.

Workload kinds (`aws.lambda`, `aws.fargate-task`) are surfaced via `workloadReports` and `supportedSourceKinds`.
Compute-platform kinds (`aws.ecs-cluster`, `aws.fargate-cluster`, `aws.eks`, `aws.ec2`) are surfaced via
`computePlatformReports`. Only `aws.ecs-cluster` is assignable via `createSource` (enables ecs-agent
deploy on the cluster).

## ECS: the EC2 / Fargate division of labor

ECS workloads are covered by two complementary solutions, split by launch type:

- **EC2 launch type** → the cluster-wide **odigos-ecs-agent** (the vm-agent's eBPF daemon, one per
  container instance; compute-platform domain, `ODIGOS_ECS_AGENT_IMAGE`). Fargate forbids eBPF (no
  privileged containers / host PID), so that agent explicitly does not support Fargate.
- **Fargate launch type** → the per-task **sidecar** this connector injects (workload domain,
  `aws.fargate-task`). Only Fargate services/tasks are discovered into the workload inventory;
  EC2-launch-type services/tasks are never stored there.

## Fargate service instrumentation (aws.fargate-task)

The unit of instrumentation is the **ECS service** (task ARNs recycle on every deploy; the service is
the stable identity: external id `cluster_arn/service_name`). Enabling a source makes each reconcile
(1m + event-triggered) converge the service's task definition to the instrumented state:

- an **`odigos-agent` sidecar** (from `ODIGOS_FARGATE_AGENTS_IMAGE`, `Essential=false`) copies the
  language agent from `/instrumentations` into a shared task volume mounted at `/var/odigos`, then exits;
  the app container `DependsOn` it with condition `SUCCESS`, so the app starts only after staging;
- the app container gets the **agent-activation env** for its language — sourced from the OSS Odigos
  distros module (`java-community`, `python-community`, `dotnet-community`, `php-community`,
  `ruby-community`; Node.js uses the upstream env-driven `@opentelemetry/auto-instrumentations-node`
  because the nodejs-community distro is OpAMP-driven) — plus the **OTLP export env**
  (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, per-signal exporters) pointed **directly at the
  connector's destination** (there is no node-local collector on Fargate);
- an **`ODIGOS_MANAGED` ownership marker** records the pre-Odigos env, so disabling the source registers
  a clean revision restoring the service exactly (rollback never touches task definitions without the
  marker — a customer's own OTel setup is never stripped);
- the converged revision is registered via `RegisterTaskDefinition` and rolled out via `UpdateService`.
  The idempotency gate compares managed state (never revision ARNs), so a steady-state service costs
  zero AWS writes — and a customer deploy (which registers a fresh, unmarked revision) is re-instrumented
  within a reconcile cycle.

### Language coverage

| Language | Activation | Export protocol | Notes |
| --- | --- | --- | --- |
| Java | `JAVA_TOOL_OPTIONS` (append) | gRPC | |
| Python | `PYTHONPATH` (append) | gRPC | |
| Node.js | `NODE_OPTIONS --require` (append) | gRPC | upstream auto-instrumentations-node |
| .NET | `CORECLR_*` / `DOTNET_*` (static) | gRPC | glibc images only (Alpine/musl unsupported in v1) |
| PHP | `PHP_INI_SCAN_DIR` + `OTEL_PHP_AUTOLOAD_ENABLED` | **http/protobuf only** | needs runtime major.minor (see below) |
| Ruby | `RUBYOPT` + `ODIGOS_GEM_PATH` | **http/protobuf only** | needs runtime major.minor (see below) |
| Go / anything eBPF | — | — | impossible on Fargate (no privileged containers); reported Unsupported |

Language resolution order: source `runtimeOverride` → previously detected → container image config (CNB
buildpack labels, then entrypoint/cmd/env — ECR images authenticate via `ecr:GetAuthorizationToken`,
public registries anonymously; optional layer) → task-definition heuristics. PHP/Ruby agent paths are
per-version: the major.minor is resolved from the override version, the image's `PHP_VERSION`/
`RUBY_VERSION` env, or the image tag (`php:8.3-fpm`) — unresolved versions report Unsupported with a
hint to set `runtimeOverride: {language: php, version: "8.3"}`.

**Export protocol is derived from each language's distro**, not assumed: every OSS community distro
declares `otlpHttpLocalNode` (it emits OTLP/HTTP), so the connector prefers the destination's
`OTLP_HTTP_ENDPOINT`. PHP and Ruby are *pinned* to http/protobuf — their community SDKs speak nothing
else — and report Unsupported without an OTLP/HTTP endpoint; the rest honor
`OTEL_EXPORTER_OTLP_PROTOCOL`, so they fall back to `OTLP_GRPC_ENDPOINT` when that is all the
destination offers. The connector accepts both `otlp` and `otlphttp` destination types, and logs a
warning at instrument time when an endpoint's port belongs to the *other* protocol (an HTTP endpoint on
`:4317`, say) — a pairing that is individually valid but fails at runtime as an opaque connection reset
inside the task.

**Multi-container task definitions** pick the application by **detected runtime, not by port mapping**.
In a proxy+app pair (nginx in front of PHP-FPM, Envoy in front of a service) the port-mapped container
is the *proxy*, which has no agent, so choosing by ingress would instrument the wrong one. Detections
are ranked by confidence, so a container that merely mentions a runtime in its command text — an nginx
config naming `index.php` — loses to one with a real signal (buildpack metadata, a runtime version env
var, an interpreter executable). A task definition with no instrumentable container, or with several
equally-strong ones, reports Unsupported naming what each container resolved to rather than guessing.

### Not instrumentable (reported Unsupported)

- **Standalone tasks** (RunTask/EventBridge — no ECS service): discoverable and selectable, but there is
  no `UpdateService` rollout path. Run the workload as an ECS service to instrument it.
- Services with **CODE_DEPLOY / EXTERNAL deployment controllers** (`UpdateService` may not change their
  task definition).
- **Windows** task definitions (the agents bundle is Linux-only; amd64 + arm64/Graviton are supported).
- Multi-container task definitions with **no single identifiable app container** (multiple essential
  containers and not exactly one with port mappings), task definitions already at the **10-container
  limit**, or ones delivering a managed env var via **ECS `secrets`**.

### Network prerequisites (reachability matters)

- **Agents-image pull**: new tasks pull `ODIGOS_FARGATE_AGENTS_IMAGE` (default
  `registry.odigos.io/odigos-fargate-agents`). The task subnet needs egress to that registry (public IP
  or NAT). A failed pull blocks the app start (the SUCCESS dependency), surfacing as a stuck deployment —
  ECS deployment circuit breaker (if enabled) rolls it back.
- **OTLP export**: instrumented tasks export **directly** to the destination endpoint — it must be
  reachable from the task's VPC over its egress path.

### IAM

Discovery: `ecs:ListClusters`, `ecs:DescribeClusters`, `ecs:ListServices`, `ecs:DescribeServices`,
`ecs:ListTasks`, `ecs:DescribeTasks`. Instrumentation additionally: `ecs:DescribeTaskDefinition`,
`ecs:RegisterTaskDefinition`, `ecs:UpdateService`, and **`iam:PassRole`** (registering a task definition
carrying a task/execution role — virtually always on Fargate — passes those roles; scope the grant with
the condition `iam:PassedToService=ecs-tasks.amazonaws.com`). Optional, detection-only:
`ecr:GetAuthorizationToken`, `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`.

### Debugging

The `debugEcsTelemetry` DB-backed config toggle (agents-api `updateConfig`, no restart) stamps
`OTEL_LOG_LEVEL=debug` onto instrumented services so the SDKs log their exporter activity. The
`odigos_task_definition` metadata on the `ecs_services` row records the revision the connector
registered, distinguishing Odigos revisions from customer deploys.

## Layout

```
cmd/main.go                    # startup: schema check -> register -> loops -> serve GraphQL
pkg/awsprovider                # wires workload + compute-platform domains into base.Provider
pkg/config                     # env config
pkg/awsclient                  # shared AWS config cache
pkg/workload/
  store/                       # lambda_functions + ecs_services + ecs_tasks inventory
  detector/                    # Lambda + Fargate (services + tasks) discovery
  instrumentor/                # Lambda instrumentation + Fargate sidecar instrumentation
pkg/computeplatform/
  store/                       # ecs_clusters inventory
  detector/                    # ECS cluster discovery + classification
  reconciler/                  # aws.ecs-cluster agent deploy (stub) + aws.eks/aws.ec2 stubs
```

## Configuration (env)

| Var | Required | Purpose |
| --- | --- | --- |
| `ODIGOS_CONNECTOR_NAME` | yes | Connector identity (discriminator across rows) |
| `DATABASE_URL` | yes | Postgres DSN |
| `ODIGOS_AWS_ACCOUNT_ID` | yes | Account boundary (verified at startup) |
| `ODIGOS_DB_SCHEMA` | no (`connector_aws`) | Provider schema |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN` | no | Static credentials |
| `ODIGOS_AWS_ROLE_ARN` / `ODIGOS_AWS_EXTERNAL_ID` | no | AssumeRole (else IRSA / default chain) |
| `ODIGOS_FARGATE_AGENTS_IMAGE` | no (`registry.odigos.io/odigos-fargate-agents:v0.1.0`) | Fargate agents bundle the sidecar stages from |
| `ODIGOS_ECS_AGENT_IMAGE` | no | odigos-ecs-agent (EC2 cluster eBPF daemon) image — a different artifact |
| `ODIGOS_AWS_LAMBDA_LAYER_ARNS` / `ODIGOS_AWS_LAMBDA_LAYER_DIR` | no | Lambda layer table / staging dir overrides |
| `ODIGOS_GQL_ADDR` | no (`:8080`) | GraphQL/health listen address |
| `ODIGOS_CLI_ADDR` | no (`:23678`) | odictl CLI WebSocket (`/ws`) listen address |

The connector scans every region enabled in the account (via `ec2:DescribeRegions`); there is no regions
setting. Discovery interval and tag selectors are DB-backed operational config (agents-api
`updateConfig`), not env.

## Artifacts this connector consumes

The OpenTelemetry Lambda layers and the Fargate agents bundle are **not built in this repository**. Both
are released by [`odigos-io/aws-connector-artifacts`](https://github.com/odigos-io/aws-connector-artifacts),
which publishes the same layer zips three ways from one build — into the image this connector reads them
from, as public Lambda layer versions in every region, and as Serverless Application Repository
applications customers deploy into their own accounts. A customer who deploys the layer themselves and a
customer whose functions this connector instruments have to get identical bytes, and that only holds if
there is one build.

| Artifact | Pinned by | Used |
| --- | --- | --- |
| `odigos-lambda-agents:vX.Y.Z` | `LAMBDA_AGENTS_IMAGE` build arg in [`Dockerfile`](Dockerfile) | build time — supplies `/var/odigos/lambda/<lang>/<arch>/layer.zip`, which the connector publishes into customer accounts |
| `odigos-fargate-agents:vX.Y.Z` | `config.DefaultFargateAgentsImage` | runtime — pulled by every instrumented Fargate task |

Picking up new upstream OpenTelemetry versions means bumping those two pins, each as its own reviewed PR.

## Build & run locally

`make build-connector-aws` from the repo root. `go build ./...` and `go test ./...` work locally.

To build against an unreleased layer bundle:

```bash
make build-connector-aws LAMBDA_AGENTS_IMAGE=my-registry/odigos-lambda-agents:dev
```

The Fargate agents image is pulled by **Fargate tasks from a registry** — it is never loaded into kind;
point the connector at a pushed tag via `ODIGOS_FARGATE_AGENTS_IMAGE` (or rely on the compiled-in default
bundle).

Smoke test against the bundled Postgres image:

```bash
# start postgres + apply migrations (see connector-postgres/README.md), then:
DATABASE_URL="postgres://postgres:odigos@127.0.0.1:5432/odigos_central?sslmode=disable" \
ODIGOS_CONNECTOR_NAME=aws-dev \
AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... \
go run ./cmd
# workload kinds: curl -s localhost:8080/graphql -d '{"query":"{ supportedSourceKinds }"}' ...
# workload reports: curl -s localhost:8080/graphql -d '{"query":"{ workloadReports { name kind region } }"}' ...
```

## Fargate E2E (kind cluster + real AWS account)

Mirrors the GCP connector's Cloud Run walkthrough (`connectors/providers/gcp/README.md` §3–8):

1. In AWS: a Fargate cluster + service from a sample Java/Node image (`aws ecs create-service
   --launch-type FARGATE …`), plus one standalone `run-task` to see the Unsupported path.
2. In kind: `make connector-dev-up`, then `make build-connector-aws load-to-kind-connector-aws TAG=dev`,
   apply the connector CR (capability `aws.fargate-task` with `discovery: true, instrumentation: true`)
   + credentials Secret.
3. Verify discovery: `select * from connector_aws.ecs_services` shows the Fargate service; EC2
   services/tasks are absent; the standalone task is in `ecs_tasks` with an empty `service_name`.
4. Run Jaeger on a public EC2 instance (the GCP README's Option A — tasks export OTLP directly), create
   an `otlp` destination pointing at it, and enable a source for the service (identity: name
   `{cluster}-{service}`, kind `aws.fargate-task`, region).
5. Watch the reconcile register revision `<family>:<n+1>` with the `odigos-agent` container and roll the
   deployment; the sidecar exits 0, the app mounts `/var/odigos`, spans arrive with
   `cloud.platform=aws_ecs`.
6. Simulate a customer deploy (register a fresh revision from the original + `UpdateService`): the
   connector re-instruments within ~1m with no env accumulation. Disable the source: a clean revision
   rolls out. Point a source at the standalone task: the report shows Unsupported.

## Inspecting with odictl

Port-forward the connector pod's CLI port and run odictl with its default port (`23678`):

```bash
make port-forward-connector-aws
cd ~/code/vm-agent/cmd/odictl && go run .
```

Sources, destinations, workload/compute-platform reports, discovery and health views are backed by the
connector; actions, rules, config and profiling are not implemented.
