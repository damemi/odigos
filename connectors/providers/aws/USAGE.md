# Using the AWS connector

A practical companion to [README.md](README.md), which covers the runtime's internal architecture. This
document covers the customer/operator-facing side: what the connector does end to end, the two ways a
workload gets instrumented, how to instrument Lambda and Fargate yourself via CloudFormation or CDK, how
Odigos publishes and how you can consume the Lambda layer through the Serverless Application Repository
(SAR), and how to build the Fargate/ECS agent images locally for development instead of pulling the
released ones.

The layer zips, the Fargate agents bundle, the SAR applications and the CDK packages are all built and
released from a **separate** repository,
[`odigos-io/aws-connector-artifacts`](https://github.com/odigos-io/aws-connector-artifacts) — this
connector only consumes their output by pinned tag/version (see
[README.md § Artifacts this connector consumes](README.md#artifacts-this-connector-consumes)). Most of
§§4–6 below live there; this doc exists so the whole story is readable from one place.

## 1. What the connector does

One connector instance owns one AWS account boundary (`spec.account.id`). It runs four independent loops
— workload discovery, compute-platform discovery, workload reconcile, compute-platform reconcile — over
every region enabled in the account. See
[README.md § Architecture](README.md#architecture-workloads-vs-compute-platforms) for the full split; in
short:

| Domain | Types | What happens |
| --- | --- | --- |
| **Workloads** | `aws.lambda`, `aws.fargate-task` | Directly instrumentable. Discovered, then optionally instrumented in place. |
| **Compute platforms** | `aws.ecs-cluster`, `aws.fargate-cluster`, `aws.eks`, `aws.ec2` | Coverage/grouping and (eventually) separate-agent install. Not instrumentable directly. |

The creation flow's **Show advanced fields → Bootstrap region** selects the region used for initial
STS identity/role-assumption and EC2 region-enumeration calls. It is persisted as
`spec.bootstrapRegion` and passed to the runtime as `ODIGOS_BOOTSTRAP_REGION`. Omitting it keeps
`us-east-1`. Choose an enabled commercial AWS region that the credentials can access.

This setting does not restrict discovery to that region. Discovery and connection checks still scan
all enabled account regions, so IAM policies must allow the selected capabilities in those regions.

## 2. Automatic vs. manual (IaC) — the two flows

Every workload type is independently switched between two modes, driven by two booleans on the connector
CR (`spec.workloads[].discovery` / `.instrumentation`):

| `discovery` | `instrumentation` | Mode | Who applies the change |
| --- | --- | --- | --- |
| `true` | `true` | **automatic** | Odigos mutates the resource directly (Lambda layer + env, or the Fargate task-definition sidecar), reconciles it every cycle, and can roll it back. |
| `true` | `false` | **manual** (default) | You apply the change yourself — CloudFormation, CDK, Terraform, `aws-cli`, or by hand. Odigos only observes and reports; it never mutates the workload. |
| `false` | — | — | The type isn't discovered at all. |

There is no separate "mode" field anywhere — it's derived from those two booleans, which is what makes it
safe across connector versions: an older image with no reconciler for a type simply can't mutate a
workload you chose to own.

A manual-mode workload reports `NotInstrumented`, with an issue coded `WaitingForCustomer`, until discovery sees the
**`ODIGOS_IAC`** marker your IaC applied (§§4–5), at which point it flips to `Instrumented` on its own —
there is nothing to click. `ODIGOS_MANAGED` is the opposite marker: the connector's own record of a mutation *it*
performed (with the rollback snapshot), written only in automatic mode. Never hand-write `ODIGOS_MANAGED`;
it claims a rollback that was never recorded. Full rules, including the ECS `prevEnv` gotcha, are in
[`odigos-enterprise/connectors/CLOUD-CONNECTORS.md`](../../CLOUD-CONNECTORS.md#what-the-connector-publishes-for-manual-mode).

**Pick one mode per workload.** A function or service instrumented by your own IaC *and* enabled as an
automatic-mode source is owned by both — every deploy reasserts one side's configuration, every reconcile
cycle reasserts the other's, and you get a new Lambda version / ECS revision on every round trip.

## 3. Creating a connector

Two ways to create the `OdigosCloudConnector` CR, both going through the same
`connectors/spec/data/aws.yaml` declaration and the same `testconnection` checks:

- **Central UI** — the 3-step create-connector wizard (provider → credentials → validate). See
  [`CLOUD-CONNECTORS.md` § End-to-end create flow](../../CLOUD-CONNECTORS.md#end-to-end-create-flow).
- **Directly**, by applying a CR + credentials Secret — see
  [`connectors/examples/aws-connector.yaml`](../../examples/aws-connector.yaml) for a fully annotated
  example. Useful for local/dev clusters or GitOps.

### Authentication

Three composable methods (`connectors/spec/data/aws.yaml` → `auth.methods`), selected by which Secret keys
you provide:

| Method | Secret keys | Notes |
| --- | --- | --- |
| `workload-identity` | *(none)* | The connector pod's ambient credentials (e.g. IRSA). |
| `static-keys` | `accessKeyId`, `secretAccessKey`, optional `sessionToken` | Long-lived or temporary IAM keys. |
| `assume-role` | `roleArn`, optional (but recommended) `externalId` | Composes on top of either base method for cross-account access. **Set an external ID before generating a create-role snippet** — a trust policy generated with a blank one omits the `ExternalId` condition entirely and the role becomes permanently unassumable. |

`sts:GetCallerIdentity` is the only permission every method needs; per-feature permissions (discovery,
instrumentation) are additive on top and listed in `aws.yaml` and in
[README.md § IAM](README.md#iam).

## 4. IaC instrumentation for Lambda

Three manual-mode paths ship as copy-paste artifacts per language in
[`connectors/spec/data/aws.yaml`](../../spec/data/aws.yaml) (`section: instrumentation:aws.lambda`):
Terraform, CloudFormation, and AWS CDK, plus a plain `aws-cli` one-off. All four are checked in CI
(`TestInstrumentationArtifactsMatchPlans`) against the same recipe the connector's own automatic-mode
injector uses, so they can't silently drift from what Odigos then judges a function against.

The example below is Java; Python, Node.js and Ruby have the same shape with different exec-wrapper paths
and activation variables — see `aws.yaml` for the language you need, or the connector's own table in
[README.md § Language coverage](README.md#language-coverage).

### CloudFormation

```yaml
# Merge into Resources.<YourFunction>.Properties
Layers:
  - "<OTEL_LAYER_ARN>"          # keep any layers you already have, add this one
Environment:
  Variables:
    ODIGOS_IAC: '{"v":1,"layer":"<OTEL_LAYER_ARN>"}'
    AWS_LAMBDA_EXEC_WRAPPER: "/opt/otel-handler"     # language-specific — see the table below
    OTEL_SERVICE_NAME: "<ODIGOS_SOURCE_NAME>"
    OPENTELEMETRY_COLLECTOR_CONFIG_URI: 'yaml:{receivers: {otlp: {protocols: {grpc: {endpoint: "localhost:4317"}, http: {endpoint: "localhost:4318"}}}}, processors: {memory_limiter: {check_interval: 1s, limit_percentage: 50, spike_limit_percentage: 20}, batch: {send_batch_size: 512, timeout: 200ms}, decouple: null}, exporters: {otlp: {endpoint: "<OTLP_ENDPOINT>", tls: {insecure: <OTLP_INSECURE>}}}, service: {telemetry: {metrics: {level: none}}, pipelines: {traces: {receivers: [otlp], processors: [memory_limiter, batch, decouple], exporters: [otlp]}}}}'
```

`ODIGOS_IAC` is mandatory (§2) and `OPENTELEMETRY_COLLECTOR_CONFIG_URI` is compared **whole-string**, so
substitute only the two placeholders — every space after a colon in that YAML flow mapping is
load-bearing (write `a:b` without the space and the collector accepts the variable, logs the URI, and
then fails with an unrelated `otelcol state is Closed`). Never add
`OTEL_EXPORTER_OTLP_ENDPOINT`/`ODIGOS_OTLP_ENDPOINT`/`ODIGOS_OTLP_INSECURE` — Odigos compares each
against *unset*.

| Runtime | `AWS_LAMBDA_EXEC_WRAPPER` |
| --- | --- |
| Python | `/opt/otel-instrument` |
| Node.js, Java, Ruby | `/opt/otel-handler` |

Java has three more wrappers for non-default handler interfaces (`/opt/otel-proxy-handler` for API
Gateway proxy events, `/opt/otel-sqs-handler` for SQS, `/opt/otel-stream-handler` for
`RequestStreamHandler`) — using the default on a `RequestStreamHandler` function breaks the invocation,
it doesn't just produce thinner spans.

Behind the [AWS Lambda Web Adapter](https://github.com/awslabs/aws-lambda-web-adapter), don't set
`AWS_LAMBDA_EXEC_WRAPPER` (the adapter owns it) — append the agent to the language's loader variable
instead, e.g. for Java: `JAVA_TOOL_OPTIONS: "<YOUR_EXISTING_VALUE> -javaagent:/opt/opentelemetry-javaagent.jar"`.

### CDK

```bash
npm install @odigos/aws-cdk@<PACKAGE_VERSION>       # Python: pip install odigos-aws-cdk==<PACKAGE_VERSION>
```

```ts
import { OdigosLambdaInstrumentation } from '@odigos/aws-cdk';

new OdigosLambdaInstrumentation(this, 'Odigos', {
  destination: { endpoint: 'otlp.example.com:4317' },   // OTLP/gRPC as bare host:port, no scheme
  serviceName: '<ODIGOS_SOURCE_NAME>',                  // pass explicitly if your stack doesn't name the function
  functions: [apiFn, workerFn],
});
```

This one construct call resolves the layer ARN for each function's language/architecture, sets the exec
wrapper, renders `OPENTELEMETRY_COLLECTOR_CONFIG_URI` with the **same renderer the connector itself
uses**, and writes `ODIGOS_IAC` for you — nothing above needs to be hand-verified byte-for-byte the way
the CloudFormation snippet does. It does **not** write `ODIGOS_MANAGED`, by design (§2).

Two ways to source the layer, chosen with `layerSource`:

| | `ODIGOS_PUBLIC` (default) | `IN_ACCOUNT` |
| --- | --- | --- |
| What it synthesizes | a layer ARN | a nested `AWS::Serverless::Application` (the SAR app, §6) per language/arch |
| Layer lives in | the Odigos AWS account | **your** account |
| Resolved | at synth time, from a table baked into the package | at deploy time, from SAR |

Use `IN_ACCOUNT` if your org's policy forbids attaching a Lambda layer owned by another AWS account — see
§6 for what that actually deploys. Full API, the `WEB_SERVER` mode, per-signal opt-in and the Java handler
distros are documented in
[`aws-connector-artifacts/cdk/README.md`](https://github.com/odigos-io/aws-connector-artifacts/blob/main/cdk/README.md).

**Aspect over a whole stack**, instrumenting every function at once (unsupported runtimes are skipped with
a synth warning, not silently):

```ts
Aspects.of(stack).add(new OdigosInstrumentationAspect(stack, {
  destination: { endpoint: 'otlp.example.com:4317' },
}));
```

## 5. IaC instrumentation for Fargate

Same idea, and the same delivery variants as Lambda except `aws-cli` — Terraform, CloudFormation and AWS
CDK, hand-authored per language in `aws.yaml` (`section: instrumentation:aws.fargate-task`). For a CDK app
there is now a construct that makes the whole change for you; see the AWS CDK subsection at the end of this
section.

### CloudFormation (Java shown)

```yaml
# Merge into Resources.<YourTaskDefinition>.Properties
Volumes:
  - Name: odigos-agent          # ephemeral-storage bind mount, scoped to the task
ContainerDefinitions:
  - Name: odigos-agent
    Image: "registry.odigos.io/odigos-fargate-agents:v0.0.18"
    Essential: false            # required: SUCCESS below is only valid against a non-essential container
    Command: ["sh", "-c", "mkdir -p /var/odigos && cp -r /instrumentations/java /var/odigos/java"]
    MountPoints:
      - SourceVolume: odigos-agent
        ContainerPath: /var/odigos
  - Name: "<YOUR_CONTAINER>"
    # ... your existing configuration ...
    MountPoints:
      - SourceVolume: odigos-agent
        ContainerPath: /var/odigos
        ReadOnly: true
    DependsOn:
      - ContainerName: odigos-agent
        Condition: SUCCESS
    Environment:
      - Name: ODIGOS_IAC
        Value: '{"v":1,"lang":"java"}'
      - Name: OTEL_SERVICE_NAME
        Value: "<ODIGOS_SOURCE_NAME>"
      - Name: OTEL_RESOURCE_ATTRIBUTES
        Value: "cloud.provider=aws,cloud.platform=aws_ecs,cloud.region=<REGION>"
      - Name: OTEL_EXPORTER_OTLP_ENDPOINT
        Value: "<OTLP_ENDPOINT_URL>"
      - Name: OTEL_EXPORTER_OTLP_PROTOCOL
        Value: "http/protobuf"           # or "grpc" — must match the endpoint's port; see note below
      - Name: OTEL_TRACES_EXPORTER
        Value: "otlp"
      - Name: OTEL_METRICS_EXPORTER
        Value: "otlp"
      - Name: OTEL_LOGS_EXPORTER
        Value: "otlp"
      # APPEND ours after any existing value, joined by a space — assigning it discards yours.
      - Name: JAVA_TOOL_OPTIONS
        Value: "<YOUR_EXISTING_VALUE> -javaagent:/var/odigos/java/javaagent.jar"
```

Things that are easy to get wrong here (each one is checked by conformance, so getting it wrong shows up
as reported drift, not a silent no-op):

- **`ODIGOS_IAC` is per container** and is the only thing marking a container as instrumented; its
  recorded `lang` is what the activation environment above is checked against.
- **Transport is one decision.** The Java/Python/Node agents prefer `http/protobuf`, but which transport
  is actually valid is decided by the destination configured on *this connector* — if it only offers
  OTLP/gRPC, use the gRPC endpoint (port 4317) and `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` instead of the
  `http/protobuf` shown above. PHP and Ruby only support `http/protobuf` at all.
- **One `odigos-agent` container serves the whole task**, so a multi-language task definition needs a
  chained `&& cp -r /instrumentations/<lang> /var/odigos/<lang>` per language, in alphabetical order —
  Odigos compares the command exactly against the union of every marked container's language.
- Register the new task-definition revision **and** `UpdateService` to it — registering alone doesn't
  roll it out.
- The image is pulled by the task's execution role at every task start; if the task can't reach
  `registry.odigos.io`, mirror the image into a registry it can reach (see §7 for building it yourself).
- 10-container task-definition limit, Linux-only, one `SUCCESS` dependency requires the stager to be
  non-essential.

### AWS CDK

`OdigosFargateInstrumentation` from `@odigos/aws-cdk` makes all four changes above — the volume, the
staging container, the mount and startup dependency, and the environment — so none of the YAML above is
copied by hand:

```ts
import { OdigosFargateInstrumentation, OdigosFargateLanguage } from '@odigos/aws-cdk';

const odigos = new OdigosFargateInstrumentation(this, 'Odigos', {
  destination: { httpEndpoint: 'https://otlp.example.com:4318' },
  // Optional — the package already defaults to its own release's bundle, and records it (see below).
  agentsImage: 'registry.odigos.io/odigos-fargate-agents:v0.0.18',
});

odigos.instrument({
  container: apiContainer,
  language: OdigosFargateLanguage.JAVA,
  service: apiService,          // OTEL_SERVICE_NAME is read off the ECS service name
  // runtimeVersion: '8.3',     // required for php and ruby
});
```

Three things it does not paper over, because they are decisions rather than boilerplate:

- **The service name has no default.** `OTEL_SERVICE_NAME` must equal the ECS *service* name, so pass
  `service` or `serviceName`. A CFN-generated service name throws at synth: it is a `Ref` to the service, and
  the task definition that service runs cannot reference it. Note this is deliberately *not* the name the
  source is listed under in Odigos — Fargate sources are named `<cluster>-<service>` so that same-named
  services in two clusters stay distinct rows, and that prefix is not expressible in a task definition. If you
  set a reported service name on the source, that value wins over the ECS service name in both modes.
- **The language is stated, not detected.** A container declares an image, not a runtime, so there is no
  aspect equivalent to the Lambda one.
- **The agents image is recorded, not assumed.** The construct writes the image it staged into the
  `ODIGOS_IAC` marker as `image`, and `desiredAgentsImage` holds the task definition to THAT rather than to
  `config.FargateAgentsImage` — the Fargate counterpart of the Lambda marker's `layer` field, and for the
  same reason: bumping `ARTIFACTS_VERSION` here must not report every manually instrumented service in the
  account as drifted for a change made in Odigos. So `agentsImage` is only needed to point at a mirror, and
  a mirror is then judged against itself. A marker that records no image at all — which is what the
  Terraform and CloudFormation snippets above produce — falls back to a signature check on the staging
  container rather than to this build's pin. Two containers recording *different* images is a conflict and
  is reported: one staging container cannot serve two bundles.

Multi-language task definitions work the way they do above: one `instrument()` call per container, one
staging container for the task definition, and the construct builds its command from the union of every
instrumented container's agents in ascending directory order — the same order the connector compares
against. Note that `ecs.ContainerDefinition` does expose `addEnvironment(name, value)`, which is what the
construct uses; what it has no reader for is an existing value, which is why loader variables get read off
the container's own props and `existingLoaderValue` exists as an override.

## 6. Publishing and consuming the Lambda layer via SAR

The layer is distributed three ways from one build in `aws-connector-artifacts` — the connector's own
image, a public layer ARN in every region, and a SAR application per (language, architecture) — so a
customer who deploys it themselves and a customer the connector instruments get identical bytes. SAR is
specifically for organisations whose policy forbids attaching a layer owned by another (Odigos-owned) AWS
account, or that requires every artifact in their functions to be an object their own CloudFormation stack
created.

### How Odigos publishes it (release pipeline)

[`scripts/publish-sar.sh`](https://github.com/odigos-io/aws-connector-artifacts/blob/main/scripts/publish-sar.sh)
runs once per release, in `us-east-1` only (a publicly-shared SAR app is deployable in every commercial
region — SAR copies the artifacts into the destination region at deploy time, so nothing here is
replicated by hand):

```bash
scripts/publish-sar.sh \
  --version v0.2.0 --layers-dir dist/layers \
  --bucket odigos-sar-artifacts --region us-east-1 \
  --out dist/sar.json [--ensure-bucket-policy] [--dry-run]
```

For each of the 8 (language × architecture) combinations it renders
[`sar/template.yaml`](https://github.com/odigos-io/aws-connector-artifacts/blob/main/sar/template.yaml),
then:

1. `sam package` — uploads `layer.zip` + `readme.md`/`LICENSE` to the artifact S3 bucket and rewrites the
   template to reference them ([`sam package` reference](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/sam-cli-command-reference-sam-package.html)).
2. `sam publish` — creates the application version from the packaged template
   ([`sam publish` reference](https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/sam-cli-command-reference-sam-publish.html)).
   `sam publish` creates applications **private by default**; SAR semantic versions are immutable, so an
   "already exists" response from a retried release is treated as success rather than an error.
3. `aws serverlessrepo put-application-policy --statements Principals=*,Actions=Deploy` — shares it
   publicly. Idempotent; the application ARN is constructed
   (`arn:aws:serverlessrepo:<region>:<account>:applications/<name>`), never looked up, because
   `ListApplications` throttles hard and there's nothing to discover — `sam publish` just created it. See
   [Resource-based policy examples](https://docs.aws.amazon.com/serverlessrepo/latest/devguide/security_iam_resource-based-policy-examples.html)
   for the policy shape.

**Prerequisite that's easy to miss:** the artifact S3 bucket must grant `serverlessrepo.amazonaws.com`
`s3:GetObject` (conditioned on `aws:SourceAccount`) — without it, `sam package`/`sam publish` fails on a
permission error that names the bucket but not the missing statement. `--ensure-bucket-policy` applies it
for you. Requires `aws` CLI v2, `sam` CLI (`aws-actions/setup-sam` in CI), and `jq`.

### How a customer deploys it into their own account

- **Console** — Serverless Application Repository → *Available applications* → *Public applications* →
  search `odigos-otel-<language>-<arch>` → **Deploy**. The stack's `LayerVersionArn` output is what you
  attach. See AWS's own [deploying-applications guide](https://docs.aws.amazon.com/serverlessrepo/latest/devguide/serverlessrepo-how-to-consume.html)
  for the console and `aws serverlessrepo create-cloud-formation-change-set` / `execute-change-set` CLI
  walkthrough.
- **SAM / CloudFormation**, as a nested application in your own template:

  ```yaml
  Odigos:
    Type: AWS::Serverless::Application
    Properties:
      Location:
        ApplicationId: arn:aws:serverlessrepo:us-east-1:<odigos-account>:applications/odigos-otel-python-arm64
        SemanticVersion: 0.1.0
  # ...
  Layers: [!GetAtt Odigos.Outputs.LayerVersionArn]
  ```

- **CDK** — `layerSource: OdigosLayerSource.IN_ACCOUNT` (§4) synthesizes exactly the nested application
  above and wires the output ARN into your function for you. Needs
  `serverlessrepo:CreateCloudFormationTemplate` and `serverlessrepo:GetApplication` on the deploying
  principal.

Superseded layer versions are retained, not deleted (a function still pinned to an older version would
break at cold start the moment it vanished) — clean up old versions yourself once nothing references them.
Full details, including the exec-wrapper table and the raw `OPENTELEMETRY_COLLECTOR_CONFIG_URI` value, are
in [`aws-connector-artifacts/sar/readme.md`](https://github.com/odigos-io/aws-connector-artifacts/blob/main/sar/readme.md)
— that file **is** the SAR application's own readme, shown on its console listing.

## 7. Local development: building agent images instead of pulling from remote

Both the Fargate agents bundle and the Lambda layer bundle are ordinary container images, built in
`aws-connector-artifacts` and published to `registry.odigos.io` / a private GCP Artifact Registry. For
local development you build them yourself and point the connector at the result instead of the released
tag.

### Fargate agents bundle (`ODIGOS_FARGATE_AGENTS_IMAGE`)

```bash
# from aws-connector-artifacts
make build-fargate-agents TAG=dev              # host-arch build, for iterating locally
make buildx-push-fargate-agents \
    REGISTRY=<your-registry> TAG=dev PLATFORMS=linux/amd64,linux/arm64
    # multi-arch + --push: Fargate runs both amd64 and Graviton tasks and pulls whichever matches
```

**This image is never loaded into a local kind cluster.** It isn't pulled by the connector or by anything
running there — it's pulled by real Fargate *tasks*, at task-start time, straight from the registry named
in `ODIGOS_FARGATE_AGENTS_IMAGE`. So "local" here means: build it locally, push it somewhere your test
Fargate tasks can actually reach (your own ECR repo, a private registry with a public endpoint, etc.),
then point the connector at that tag instead of the compiled-in default
(`registry.odigos.io/odigos-fargate-agents:v0.0.18`, see `config.DefaultFargateAgentsImage`):

```bash
ODIGOS_FARGATE_AGENTS_IMAGE=<your-registry>/odigos-fargate-agents:dev
```

Set it on the connector Deployment (env, or `spec.env` on the `OdigosCloudConnector` CR if wired there) —
`connectors/providers/aws/README.md` § *Fargate E2E* walks through a full kind + real-AWS-account test
using this override. If you don't already have a scratch registry to push dev images to, any repo the
Fargate task's execution role can pull from works — a private ECR repo in the same account is the usual
choice; `push-connector-aws` in the root `Makefile` shows the equivalent `docker buildx build --push`
shape for the connector's own image.

### ECS-EC2 agent image (`ODIGOS_ECS_AGENT_IMAGE`)

**There is currently nothing to build or load here.** `ECSAgentImage`/`ODIGOS_ECS_AGENT_IMAGE` is accepted
by the connector's config and threaded through to
[`pkg/computeplatform/reconciler/ecs_agent.go`](pkg/computeplatform/reconciler/ecs_agent.go),
but the actual ECS deploy (register a task definition, run the agent as a daemon service, wire rollback)
is **stubbed** in this release — the reconciler only logs the image and connection parameters it *would*
use. Setting the env var changes what gets logged; it does not start anything. Watch
`connectors/providers/aws/README.md` § *ECS: the EC2 / Fargate division of labor* for when the real deploy
lands, at which point this section should gain the same "build it, push it, point the env var at it"
recipe as Fargate above.

### Lambda layer bundle (`LAMBDA_AGENTS_IMAGE`, build-time only)

Not runtime-pulled — the zips are baked into the `connector-aws` image at *build* time and the connector
publishes them into customer accounts itself (`lambda:PublishLayerVersion`). To build the connector against
a layer bundle you built locally instead of the pinned release:

```bash
# from aws-connector-artifacts
make build-lambda-agents TAG=dev      # assembles the layers into the odigos-lambda-agents image
make extract-layers TAG=dev           # optional: copies the zips out to dist/layers for inspection

# from odigos-enterprise, pointing the connector build at your local image
make build-connector-aws LAMBDA_AGENTS_IMAGE=<your-registry>/odigos-lambda-agents:dev
```

`ARTIFACTS_VERSION` pins the released bundle both pins derive from
(`connectors/providers/aws/ARTIFACTS_VERSION`); an explicit `LAMBDA_AGENTS_IMAGE` override bypasses that
version check entirely, which is the point of having one.

## 8. Where things live

| Topic | Repository / path |
| --- | --- |
| Connector runtime, architecture, IAM | `odigos-enterprise/connectors/providers/aws/` ([README.md](README.md)) |
| Feature-wide architecture, create flow, IaC marker semantics | `odigos-enterprise/connectors/CLOUD-CONNECTORS.md` |
| Auth methods, discovery/instrumentation types, per-language IaC artifacts | `odigos-enterprise/connectors/spec/data/aws.yaml` |
| Example connector CR | `odigos-enterprise/connectors/examples/aws-connector.yaml` |
| Lambda layers, Fargate agents bundle, SAR templates, CDK packages, release pipeline | [`odigos-io/aws-connector-artifacts`](https://github.com/odigos-io/aws-connector-artifacts) |

### Developing SDK selections across local checkouts

The SDK catalog and Java adapter metadata use the next agents-api schema (`v0.28.0`). To test
uncommitted changes, copy `go.work.example` to `go.work` in the enterprise repository root. The workspace
uses the sibling `../agents-api/go/server` module instead of the published dependency. Generate the
server and both clients in agents-api after changing its schema/operations.

Build the local artifact bundle and connector without publishing either dependency:

```sh
make -C ../aws-connector-artifacts build-lambda-agents REGISTRY=odigos-local TAG=dev
python3 ../aws-connector-artifacts/scripts/sync-connector-distros.py .
LAMBDA_AGENTS_IMAGE=odigos-local/odigos-lambda-agents:dev \
  bash connectors/providers/aws/build-local.sh
```

`AGENTS_API_SRC`, `CONNECTOR_IMAGE`, and `TARGETARCH` can override the sibling checkout, output image,
and Linux architecture. The script creates an isolated temporary Go workspace and leaves module pins
unchanged. For a release, publish agents-api first and advance the connector's `go/server` dependency;
publish the matching artifacts/CDK package and UI-kit before advancing their consumer pins.

For Central UI development, build `../ui-kit` and copy its `lib` directory into
`central-ui/node_modules/@odigos/ui-kit/lib`, then build Central UI. The adapter patches the new selections
only for schema `v0.28.0` or newer, so older agents continue using their frozen GraphQL dialects.
