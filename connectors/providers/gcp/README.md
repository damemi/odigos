# connector-gcp

The GCP provider connector runtime. It implements the provider-agnostic `base.Provider` seam
(`connectors/base`) for Google Cloud across two domains:

- **Workloads** (directly instrumentable via a Source)
  - `gcp.cloud-run` — Cloud Run services. Discovery **and** instrumentation are implemented.
  Instrumentation injects the OpenTelemetry agent into the service revision the same non-eBPF way
  Odigos does on Kubernetes (eBPF is unavailable on Cloud Run): an init-ordered sidecar (from the
  busybox-based **Cloud Run agents image**, built from `agents/Dockerfile`) stages the language agent
  into a shared in-memory volume, and the ingress container is configured with the agent-activation +
  OTLP-export env vars so it loads the agent and exports directly to the connector's destination. Because
  Cloud Run has no odiglet/OpAMP server, the image ships only **env-driven** agents (Node uses the
  upstream `@opentelemetry/auto-instrumentations-node`, not the OpAMP-driven Odigos `nodejs-community`
  distro). Supported languages: **Java, Python, Node.js, .NET**.
- **Compute platforms** (managed infra Odigos is installed onto, not instrumented directly)
  - `gcp.gke` — Google Kubernetes Engine clusters (odiglet install target once installation lands).
  - `gcp.gce` — Compute Engine instances (VM agent install target once installation lands).

The base owns the discovery/reconcile loops, the agents-api server, the Central client, and
source/destination management. This module supplies the connector spec (`connectors/spec/data/gcp.yaml`),
per-type discovery (detectors) and the (currently inert) reconciler/installation seams.

## Layout

```
cmd/main.go                          entry point: config -> base store -> base.Run(gcpprovider)
pkg/config                           env config (project id, credentials) over base config
pkg/gcpclient                        client factory + project boundary verification
pkg/gcpprovider                      base.Provider implementation (wires detectors/reconcilers)
pkg/types                            spec type-name constants (gcp.cloud-run, gcp.gke, gcp.gce)
pkg/workload/{detector,instrumentor,store}         Cloud Run discovery + inventory + agent injection
pkg/computeplatform/{detector,reconciler,store}    GKE/GCE discovery + inventory (+ stub reconcilers)
```



## Configuration (stamped by connector-runtime)

- `ODIGOS_GCP_PROJECT_ID` — the project boundary (`spec.account.id`). All API calls are scoped to it.
- `ODIGOS_GCP_CREDENTIALS_JSON` — optional service account key JSON. Empty uses Application Default
Credentials (GKE Workload Identity / metadata server).
- `ODIGOS_CLOUDRUN_AGENT_IMAGE` — optional. The **Cloud Run agents image** the instrumentor stages the
language agent files from (default `us-central1-docker.pkg.dev/odigos-cloud/components/cloud-run-agents:v0.1.0`).
This is a dedicated, serverless-oriented image (built from [`agents/Dockerfile`](./agents/Dockerfile)) that
ships only **env-driven** agents — unlike the odiglet `odigos-agents` image, it does not include the
OpAMP-driven distros (e.g. `nodejs-community`) that can't run without an odiglet/OpAMP server.
**Cloud Run only pulls from `[region.]gcr.io`, `[region-]docker.pkg.dev` (Artifact Registry), or
`docker.io`**, so publish it to Artifact Registry. Build + push via the `agents/Makefile`:
  > ```bash
  > cd connectors/providers/gcp/agents
  > gcloud auth configure-docker us-central1-docker.pkg.dev
  > make build-push                 # defaults to .../components/cloud-run-agents:v0.1.0
  > # or: make build-push REGISTRY=<region>-docker.pkg.dev/<project>/<repo> TAG=v0.2.0
  > ```
  Override `ODIGOS_CLOUDRUN_AGENT_IMAGE` to point at a different tag/mirror.
- Plus the generic base env (`ODIGOS_CONNECTOR_NAME`, `ODIGOS_DB_SCHEMA=connector_gcp`,
`ODIGOS_CAPABILITIES`, `DATABASE_URL`, `ODIGOS_CENTRAL_BACKEND_URL`, …).

Unlike AWS (which enumerates enabled regions), GCP list APIs accept a location wildcard (`.../locations/-`)
and Compute Engine uses an aggregated list, so a single call per type spans every location/zone in the
project.

## Build

```
make build-connector-gcp
```



## End-to-end workflow (kind dev cluster)

This walks through provisioning GCP access, deploying the connector into a local kind
cluster, and verifying discovery. All Odigos steps are `make` + `kubectl` (the central
stack itself is installed once via Helm — `make connector-dev-up` — and reused after that).

### 1. Prerequisites

- A running `kind` cluster with the Odigos central stack (`central-backend`, `connector-runtime`,
`odigos-connector-postgres`) in the `odigos-central` namespace. Bootstrap once with:
  ```
  make connector-dev-up TAG=dev ONPREM_TOKEN=<token> GITHUB_TOKEN=<pat>
  ```
- `GITHUB_TOKEN` — a GitHub PAT with access to odigos-io private repos (needed to build images).
- `gcloud` CLI authenticated against the target project.
- At least one **running Cloud Run service** in the target project to discover and instrument. If you don't
  have one, deploy a sample from
  [GoogleCloudPlatform/cloud-run-samples](https://github.com/GoogleCloudPlatform/cloud-run-samples) (Java,
  Node.js, Python, and .NET samples are available — the languages the connector can instrument), e.g.:
  ```
  gcloud run deploy helloworld-node --source . --region us-east1 --allow-unauthenticated
  ```



### 2. Provision GCP access

The connector needs access scoped to one project. Discovery requires read/list; Cloud Run
instrumentation additionally needs `run.services.update` (to inject the agent into the service revision)
and `iam.serviceAccounts.actAs` (to update a service that runs as a runtime service account):

| Domain                        | Permissions                                          |
| ----------------------------- | ---------------------------------------------------- |
| Auth baseline                 | `resourcemanager.projects.get`                       |
| Cloud Run (discovery)         | `run.services.list`, `run.services.get`              |
| Cloud Run (instrumentation)   | `run.services.get`, `run.services.update`, `run.operations.get`, `iam.serviceAccounts.actAs` |
| Language detection (optional) | `artifactregistry.repositories.downloadArtifacts` (`roles/artifactregistry.reader`) |
| GKE (compute platform)        | `container.clusters.list`, `container.clusters.get`  |
| GCE (compute platform)        | `compute.instances.list`                             |

> **`iam.serviceAccounts.actAs` is required for instrumentation.** Updating a Cloud Run service triggers a
> second IAM check beyond `run.services.update`: the caller must be allowed to *act as* the service's
> runtime service account (the default `PROJECTNUM-compute@developer.gserviceaccount.com` unless a custom
> one is set). Granting this permission in the project-level custom role covers acting as any service
> account in the project; alternatively bind `roles/iam.serviceAccountUser` on just the specific runtime
> SA. Without it, instrumentation fails with `PermissionDenied ... iam.serviceaccounts.actAs`.

> The Artifact Registry read permission is only used to read the service image's Cloud Native Buildpack
> metadata for automatic language detection. It is optional — the source runtime override and the static
> image/command/env heuristics work without it.


Enable the APIs and create a service account with a custom role holding exactly those permissions:

```bash
export PROJECT_ID=<your-gcp-project-id>
gcloud config set project "$PROJECT_ID"

gcloud services enable \
  cloudresourcemanager.googleapis.com run.googleapis.com \
  container.googleapis.com compute.googleapis.com iam.googleapis.com

gcloud iam service-accounts create odigos-connector \
  --display-name="Odigos Cloud Connector"
export SA_EMAIL="odigos-connector@${PROJECT_ID}.iam.gserviceaccount.com"

cat > /tmp/odigos-connector-role.yaml <<'EOF'
title: Odigos Cloud Connector
description: Permissions required by the Odigos cloud connector.
stage: GA
includedPermissions:
  - resourcemanager.projects.get
  - run.services.list
  - run.services.get
  - run.services.update
  - run.operations.get
  # Required for instrumentation: act as the Cloud Run service's runtime service account when updating it.
  - iam.serviceAccounts.actAs
  # Optional: buildpack-metadata language detection (also covered by roles/artifactregistry.reader).
  - artifactregistry.repositories.downloadArtifacts
  - container.clusters.list
  - container.clusters.get
  - compute.instances.list
EOF

gcloud iam roles create odigosCloudConnector --project="$PROJECT_ID" \
  --file=/tmp/odigos-connector-role.yaml
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:${SA_EMAIL}" \
  --role="projects/${PROJECT_ID}/roles/odigosCloudConnector"

gcloud iam service-accounts keys create /tmp/odigos-connector-key.json \
  --iam-account="$SA_EMAIL"
```

> `roles/viewer` also covers these permissions if you prefer a quick, broad grant — in that
> case skip the custom role above and instead run:
>
> ```bash
> gcloud projects add-iam-policy-binding "$PROJECT_ID" \
>   --member="serviceAccount:${SA_EMAIL}" \
>   --role="roles/viewer"
> ```

> Note: A kind cluster has no ambient GCP identity, so use a **service account key** (above). On a
> real GKE cluster you can instead bind the SA via Workload Identity and leave the Secret empty
> (the `workload-identity` auth method).



### 3. (Optional) Pre-flight test without Kubernetes

Validate credentials and permissions in-process before deploying:

```bash
cd connectors/testconnection
TEST_GCP_PROJECT="$PROJECT_ID" \
GOOGLE_APPLICATION_CREDENTIALS=/tmp/odigos-connector-key.json \
GOWORK=off go test -run TestGCPManual -v .
```



### 4. Build + load the connector image into kind

(This step is included in `make connector-dev-up` but it is useful if you make changes and want to redeploy.)

`ORG` defaults to `registry.odigos.io`; use `TAG=dev` to match the running `connector-runtime`
(`CONNECTOR_GCP_IMAGE=registry.odigos.io/odigos-enterprise-connector-gcp:dev`):

```bash
make build-connector-gcp load-to-kind-connector-gcp TAG=dev GITHUB_TOKEN=$GITHUB_TOKEN
```



### 5. Create the credentials Secret + apply the connector

The Secret key must be `credentialsJson` (it matches the `gcp.yaml` auth field, injected as
`ODIGOS_GCP_CREDENTIALS_JSON`):

```bash
kubectl create secret generic odigos-connector-gcp-prod-credentials \
  -n odigos-central \
  --from-file=credentialsJson=/tmp/odigos-connector-key.json

# apply the OdigosCloudConnector CR (edit spec.account.id to your project first)
sed 's/id: "my-gcp-project"/id: "'"$PROJECT_ID"'"/' \
  ../../examples/gcp-connector.yaml \
  | awk '/^apiVersion: odigos.io/{p=1} p' \
  | kubectl apply -f -
```

`connector-runtime` reconciles the CR into a Deployment + Service named
`odigos-connector-gcp-<cr-name>-<hash>` (not `gcp-prod`), so look them up by label rather than by a fixed
name:

```bash
kubectl -n odigos-central get deploy,svc -l odigos.io/provider=gcp
```

The example pins
`spec.runtime.image` to the `:dev` image you built and loaded in step 4; without a resolvable image the CR
reports `phase: Error` with `reason: ImageUnresolved`. (In a managed install the controller supplies a
default via `CONNECTOR_GCP_IMAGE` and you can drop `spec.runtime.image` from the CR.)

### 6. Verify discovery

```bash
kubectl -n odigos-central get deploy,pods -l odigos.io/provider=gcp -w
kubectl -n odigos-central logs -l odigos.io/provider=gcp -f

# discovered inventory (connector_gcp schema)
kubectl -n odigos-central exec deploy/odigos-connector-postgres -- \
  psql -U postgres -c '\dt connector_gcp.*'
```



### 7. Instrument a Cloud Run service

Instrumentation is destination-gated and driven by an enabled Source (the same flow as AWS Lambda). The
steps below stand up a **Jaeger** OTLP destination and instrument a service against it.

> **Reachability matters.** Unlike Kubernetes workloads, an instrumented Cloud Run service exports OTLP
> **directly from GCP to the destination endpoint** — there is no node-local collector in between. So the
> `OTLP_GRPC_ENDPOINT` you configure must be reachable **from Cloud Run over the public internet**. A
> Jaeger running in your local kind cluster is not, so you either run Jaeger on a **GCE instance** with a
> public IP (Option A — no tunnel) or expose a local kind Jaeger through a **TCP tunnel** (Option B).

#### 7.1 Stand up a Jaeger reachable from Cloud Run

##### Option A — Jaeger on a GCE instance (recommended; no tunnel)

A GCE VM has a stable external IP Cloud Run can reach directly, so there is no tunnel or third-party
account to manage. Run Jaeger all-in-one on a Container-Optimized OS VM (Docker is preinstalled) and open
the OTLP + UI ports:

```bash
ZONE=us-central1-a

# 1. VM tagged so the firewall rule below targets only it.
gcloud compute instances create jaeger-otlp \
  --project "$PROJECT_ID" --zone "$ZONE" \
  --machine-type e2-small \
  --image-family cos-stable --image-project cos-cloud \
  --tags jaeger-otlp

# 2. Firewall: OTLP gRPC (4317) + UI (16686). 0.0.0.0/0 is DEV-ONLY — it exposes these ports to the whole
#    internet; restrict --source-ranges to trusted CIDRs for anything shared or long-lived.
gcloud compute firewall-rules create allow-jaeger-otlp \
  --project "$PROJECT_ID" \
  --allow tcp:4317,tcp:16686 \
  --target-tags jaeger-otlp \
  --source-ranges 0.0.0.0/0

# 3. Start Jaeger on the VM.
gcloud compute ssh jaeger-otlp --project "$PROJECT_ID" --zone "$ZONE" --command '
  docker run -d --name jaeger --restart=always \
    -e COLLECTOR_OTLP_ENABLED=true \
    -p 4317:4317 -p 16686:16686 \
    jaegertracing/all-in-one:1.60'

# 4. External IP — your destination host (endpoint = <EXTERNAL_IP>:4317, UI = http://<EXTERNAL_IP>:16686).
gcloud compute instances describe jaeger-otlp --project "$PROJECT_ID" --zone "$ZONE" \
  --format='get(networkInterfaces[0].accessConfigs[0].natIP)'
```

> **Security:** opening 4317/16686 to `0.0.0.0/0` lets anyone on the internet push spans to — and read —
> your Jaeger. For a shared setup restrict `--source-ranges`, or keep the VM internal-only and give Cloud
> Run **Direct VPC egress** (or a Serverless VPC Access connector) so it reaches the VM's *internal* IP;
> then the firewall only needs to allow your VPC ranges. Delete the VM + firewall rule when done (step 8).

Skip to 7.2 with `<EXTERNAL_IP>:4317` as the endpoint.

##### Option B — Local kind Jaeger + ngrok tunnel

Deploy Jaeger into the kind cluster:

```bash
kubectl apply -n odigos-central -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jaeger
spec:
  replicas: 1
  selector: { matchLabels: { app: jaeger } }
  template:
    metadata: { labels: { app: jaeger } }
    spec:
      containers:
        - name: jaeger
          image: jaegertracing/all-in-one:1.60
          env:
            - { name: COLLECTOR_OTLP_ENABLED, value: "true" }
          ports:
            - { containerPort: 4317 }   # OTLP gRPC (ingest)
            - { containerPort: 16686 }  # web UI
---
apiVersion: v1
kind: Service
metadata:
  name: jaeger
spec:
  selector: { app: jaeger }
  ports:
    - { name: otlp-grpc, port: 4317, targetPort: 4317 }
    - { name: ui,        port: 16686, targetPort: 16686 }
EOF
```

Then forward the ingest port locally and open a **TCP** tunnel so GCP can reach it (gRPC needs raw TCP, so
an HTTP-only tunnel will not work; `ngrok tcp` is used here as an example):

> `ngrok tcp` requires a (free) account: sign up at https://dashboard.ngrok.com/signup, then run
> `ngrok config add-authtoken <token>` once. Also make sure nothing else is already bound to `4317`
> (`lsof -i :4317`) or the port-forward will fail with `address already in use`.

```bash
kubectl -n odigos-central port-forward svc/jaeger 4317:4317 &
sleep 2   # let the forward bind before ngrok dials it
ngrok tcp 4317
# note the forwarding address, e.g. tcp://6.tcp.ngrok.io:14732
# the endpoint to use below is the host:port WITHOUT the scheme, e.g. 6.tcp.ngrok.io:14732
```

#### 7.2 Create the OTLP destination

Point it at the Jaeger `host:port` from step 7.1 — Option A: `<EXTERNAL_IP>:4317`; Option B: the ngrok
`host:port`. Jaeger is traces-only, so declare just `TRACES` (the connector automatically sets the
metrics/logs exporters to `none`). Create it via the connector's GraphQL API (port-forward the connector
Service — looked up by label, since its name is hashed), or the Central UI:

```bash
SVC=$(kubectl -n odigos-central get svc -l odigos.io/provider=gcp -o jsonpath='{.items[0].metadata.name}')
kubectl -n odigos-central port-forward "svc/$SVC" 8080:8080 &

curl -s http://localhost:8080/graphql -H 'content-type: application/json' -d '{
  "query": "mutation($input:CreateDestinationInput!){createDestination(input:$input){name type signals}}",
  "variables": {
    "input": {
      "name": "jaeger-local",
      "type": "otlp",
      "config": {
        "OTLP_GRPC_ENDPOINT": "<PUBLIC_HOST>:<PORT>",
        "OTLP_GRPC_TLS_ENABLED": "false"
      },
      "signals": ["TRACES"]
    }
  }
}'
```

> `OTLP_GRPC_TLS_ENABLED: "false"` makes the agent export over plaintext gRPC (`http://` scheme). The
> Jaeger OTLP port is plaintext, and both the GCE external IP (Option A) and an `ngrok tcp` tunnel
> (Option B) forward raw TCP to it, so TLS stays off end-to-end.

#### 7.3 Enable the capability + a Source

1. **Enable the `gcp.cloud-run` instrumentation capability** on the CR:
   ```yaml
   workloads:
     - type: gcp.cloud-run
       discovery: true
       instrumentation: true
   ```
2. **Enable a Source** for the discovered Cloud Run service. The connector resolves the language in this
   order: **source runtime override** → previously detected language → **Cloud Native Buildpack metadata**
   read from the image config (the definitive signal for `gcloud run deploy --source` builds) → static
   image/command/env heuristics. For images that aren't buildpack-built and don't otherwise reveal the
   runtime, set a **runtime language override** on the Source; the override always wins.

Sources are created/updated over the connector's GraphQL API (port-forward the connector Service, looked
up by label since its name is hashed), or via the Central UI:

```bash
SVC=$(kubectl -n odigos-central get svc -l odigos.io/provider=gcp -o jsonpath='{.items[0].metadata.name}')
kubectl -n odigos-central port-forward "svc/$SVC" 8080:8080 &
```

The Source identity is `name` (the Cloud Run service name) + `kind` (`gcp.cloud-run`) + `region` (the
service's location). List discovered services first if unsure:

```bash
kubectl -n odigos-central exec deploy/odigos-connector-postgres -- \
  psql -U postgres -c "select service_id, location from connector_gcp.cloud_run_services;"
```

**Create** a Source with a manual runtime override (enable + set the language in one call):

```bash
curl -s http://localhost:8080/graphql -H 'content-type: application/json' -d '{
  "query": "mutation($input:CreateSourceInput!){createSource(input:$input){name kind region enabled config{runtimeOverride{language version}}}}",
  "variables": {
    "input": {
      "name": "helloworld",
      "kind": "gcp.cloud-run",
      "region": "us-east1",
      "enabled": true,
      "config": { "runtimeOverride": { "language": "python" } }
    }
  }
}'
```

**Update** an existing Source to set/change the override (identity must match the existing row):

```bash
curl -s http://localhost:8080/graphql -H 'content-type: application/json' -d '{
  "query": "mutation($input:UpdateSourceInput!){updateSource(input:$input){name config{runtimeOverride{language version}}}}",
  "variables": {
    "input": {
      "name": "helloworld",
      "kind": "gcp.cloud-run",
      "region": "us-east1",
      "config": { "runtimeOverride": { "language": "python" } }
    }
  }
}'
```

Accepted `language` values (aliases normalize to the connector's four agents):

| Agent   | Accepted values                                                        |
| ------- | --------------------------------------------------------------------- |
| Java    | `java`, `jvm`, `kotlin`, `scala`                                       |
| Python  | `python`, `py`                                                        |
| Node.js | `nodejs`, `node`, `node.js`, `javascript`, `js`, `typescript`, `ts`   |
| .NET    | `dotnet`, `.net`, `net`, `csharp`, `c#`                               |

`version` is optional and informational — it does not change which agent is injected. Confirm the override
took in the connector logs (`instrumenting cloud-run service ... language=python`).

Each reconcile the instrumentor injects, into the service revision:

- an **init-ordered `odigos-agent` sidecar** (from `ODIGOS_CLOUDRUN_AGENT_IMAGE`) that copies the
  language agent into a shared in-memory volume (`/var/odigos`),
- a **shared in-memory volume** mounted into the ingress container,
- **agent-activation env** (e.g. `JAVA_TOOL_OPTIONS`, `PYTHONPATH`, `NODE_OPTIONS`, or the .NET
  `CORECLR_*`/`DOTNET_*` vars) + **OTLP export env** (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`,
  per-signal exporters) pointed at the destination,
- an `ODIGOS_MANAGED` ownership marker recording the pre-Odigos env so **disabling the Source rolls the
  service back** to exactly its original definition.

Verify on the service:

```bash
gcloud run services describe <service> --region <region> \
  --format='value(spec.template.spec.containers[].name)'   # includes odigos-agent
```

#### 7.4 View traces

Send some traffic to the Cloud Run service, then open the Jaeger UI:

```bash
# Option A (GCE): browse http://<EXTERNAL_IP>:16686 directly (16686 is open via the firewall rule).

# Option B (kind): port-forward the UI first.
kubectl -n odigos-central port-forward svc/jaeger 16686:16686
# browse http://localhost:16686
```

Pick your service (`OTEL_SERVICE_NAME`) from the Service dropdown. If nothing shows up, check (in order):
the connector logs for `instrumenting cloud-run service`, that the Cloud Run revision has the
`odigos-agent` container, that the destination endpoint is reachable from Cloud Run (Option A: the
firewall rule allows 4317; Option B: the tunnel is still up), that the service actually received
requests, and — if the agent logs show `Canceled`/`DEADLINE_EXCEEDED` on export — **CPU throttling**
(see [7.6](#76-cpu-throttling-and-telemetry-export)).

#### 7.5 Verbose agent logging (debug)

By default the injected agents log nothing on a healthy start — the OpenTelemetry SDKs only emit their
diagnostic output (startup banner, loaded instrumentations, and **exporter success/failure**) when
`OTEL_LOG_LEVEL` is set. When traces aren't showing up, turn on the connector's `debugTelemetry` toggle:
the instrumentor then stamps `OTEL_LOG_LEVEL=debug` onto every instrumented service and keeps it applied
(a manual `gcloud run services update ... OTEL_LOG_LEVEL=debug` gets stripped on the next reconcile).

Flip it live over the connector's GraphQL API (no connector restart — `updateConfig` triggers an immediate
reconcile, which rolls a new revision carrying the flag):

```bash
SVC=$(kubectl -n odigos-central get svc -l odigos.io/provider=gcp -o jsonpath='{.items[0].metadata.name}')
kubectl -n odigos-central port-forward "svc/$SVC" 8080:8080

# enable
curl -s http://localhost:8080/graphql -H 'content-type: application/json' \
  -d '{"query":"mutation{updateConfig(input:{config:{debugTelemetry:true}}){config}}"}'

# confirm it took (response should include debugTelemetry: true)
curl -s http://localhost:8080/graphql -H 'content-type: application/json' \
  -d '{"query":"query{config}"}'
```

Send some traffic, then read the service logs and look for the agent's export activity:

```bash
curl -s https://<service-url>/ >/dev/null
gcloud run services logs read <service> --region <region> --limit 200 \
  | grep -iE "otlp|export|grpc|span|error|econnrefused|deadline|unavailable"
```

Interpreting what you see:

- Startup banner + `Loading instrumentation for @opentelemetry/instrumentation-http`/`-express` → the agent
  loaded and is instrumenting (rules out a bad `NODE_OPTIONS`/register path or an ESM-only app the
  `--require` hook can't reach).
- `OTLPExporter` / `UNAVAILABLE` / `deadline` / `ECONNREFUSED` → the agent runs but can't reach the
  destination; verify the OTLP endpoint is reachable from Cloud Run (firewall/tunnel up, correct
  `host:port`, and the protocol matches the port — gRPC on 4317).
- No OpenTelemetry lines at all → the register hook isn't loading (check `NODE_OPTIONS` and that the
  `odigos-agent` sidecar staged the files into `/var/odigos`).

Turn it back off when done:

```bash
curl -s http://localhost:8080/graphql -H 'content-type: application/json' \
  -d '{"query":"mutation{updateConfig(input:{config:{debugTelemetry:false}}){config}}"}'
```

> **Java note:** the OpenTelemetry **Java agent ignores `OTEL_LOG_LEVEL`** — it only logs its startup
> banner (`opentelemetry-javaagent - version: ...`) at INFO. To get verbose export logs from Java, set
> `OTEL_JAVAAGENT_DEBUG=true` (e.g. `gcloud run services update <service> --region <region>
> --update-env-vars OTEL_JAVAAGENT_DEBUG=true`). The SDK-based agents (Python, Node.js) do honor
> `OTEL_LOG_LEVEL`.

#### 7.6 CPU throttling and telemetry export

By default Cloud Run allocates **CPU only while a request is being processed**
(`run.googleapis.com/cpu-throttling: "true"`). The OpenTelemetry agents export on a **background thread on
a timer** (the batch span/metric processors flush every few seconds), which typically fires *after* the
request finishes — at which point the instance's CPU is throttled and the in-flight OTLP call is frozen
until the next request. The tell-tale symptom is the agent loading and creating spans, but export failing
with:

- Java: `GrpcExporter - Failed to export ... The request could not be executed. Error message: Canceled`
- Node.js / Python: `OTLPExporter ... DEADLINE_EXCEEDED` / `deadline`

Spans created *during* a request may still get out if they flush before the response returns, so this
often looks like "some/no traces, intermittently."

**Fix — keep CPU allocated** so the exporter's background thread can run:

```bash
gcloud run services update <service> --region <region> --no-cpu-throttling
```

Then send traffic and confirm the `Canceled`/`deadline` errors are gone and traces land in Jaeger. Note
that always-allocated CPU changes the service's billing model (you pay for CPU for the instance's full
lifetime, not just during requests), so weigh it against your cost expectations.

> The connector does **not** currently change the CPU allocation of instrumented services — it is left as
> the service owner configured it. If your instrumented services scale to zero / throttle CPU between
> requests, set `--no-cpu-throttling` (or enable minimum instances) so telemetry exports reliably.

#### 7.7 Scale-to-zero and tail spans (min instances)

Even with CPU allocated, a service with `minScale: 0` and sporadic traffic loses the **last** spans: a span
created during a request is buffered by the batch processor and flushed a few seconds later, but by then
the idle instance may already be gone. The agent falls back to a **flush on SIGTERM** (Cloud Run gives
~10s of CPU-allocated grace before SIGKILL), which recovers most of it — but that window is bounded and
best-effort (a slow/unreachable backend, an eviction, or a crash skips it), and Cloud Run's in-memory
buffer is not durable, so nothing survives a hard kill.

To make traces reliable on low-traffic services, keep at least one instance warm so the batch flush always
runs while the instance is alive:

```bash
gcloud run services update <service> --region <region> --min-instances=1
```

This is the most robust option for latency-sensitive or bursty services, at the cost of always running (and
paying for) one instance. A lighter-weight alternative that keeps `minScale: 0` is to shorten the batch
delay so spans flush mid-request instead of relying on the shutdown window, e.g.
`--update-env-vars OTEL_BSP_SCHEDULE_DELAY=1000` (milliseconds; note the connector strips unmanaged vars on
the next reconcile).

> The connector does **not** set `min-instances` or the batch delay on instrumented services — they are
> left as the owner configured them. For reliable tracing from scale-to-zero services, pair
> `--no-cpu-throttling` with either `--min-instances=1` or a short `OTEL_BSP_SCHEDULE_DELAY`.

### 8. Iterate / clean up

```bash
# rebuild + reload + rollout restart the connector after code changes
make deploy-connector-gcp TAG=dev GITHUB_TOKEN=$GITHUB_TOKEN

# remove just the connector (keeps the central stack up)
kubectl delete odigoscloudconnector gcp-prod -n odigos-central
kubectl delete secret odigos-connector-gcp-prod-credentials -n odigos-central

# remove the Jaeger + destination used for testing
# Option A (GCE): delete the VM and firewall rule.
gcloud compute instances delete jaeger-otlp --project "$PROJECT_ID" --zone "$ZONE" --quiet
gcloud compute firewall-rules delete allow-jaeger-otlp --project "$PROJECT_ID" --quiet
# Option B (kind): delete the in-cluster Jaeger.
kubectl delete deploy/jaeger svc/jaeger -n odigos-central
# (delete the destination via the Central UI, or the deleteDestination GraphQL mutation)

# revoke the GCP key when done
rm -f /tmp/odigos-connector-key.json
```

> Disabling or deleting the Source rolls the instrumented service back to its original definition (via the
> `ODIGOS_MANAGED` marker). Delete the destination only after the rollback reconcile has run, so the
> connector can cleanly revert any still-instrumented services first.

