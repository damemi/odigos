# AWS connector usage

The maintained customer guides are [AWS setup](../../../docs/cloud-connectors/add-connectors/add-aws-connector.mdx),
[Lambda instrumentation](../../../docs/cloud-connectors/aws/workloads/lambda/instrument.mdx),
[Fargate instrumentation](../../../docs/cloud-connectors/aws/workloads/fargate/instrument.mdx), and
[regional Collectors](../../../docs/cloud-connectors/aws/regional-collector.mdx).
The same guides are maintained in the `odigos` website repository; keep the two copies synchronized.
For the provider implementation and configuration variables, see [README.md](README.md).

## Authentication and installation

Choose workload identity, access keys, or Assume Role composed with either base method. Use the source
principal ARN in the target role's trust policy. The base identity needs `sts:AssumeRole` on the target
role. The External ID must match in both places; it is a customer-specific identifier, not a secret.
The generated setup scripts embed the selected permissions policy, so no separate `odigos-policy.json`
step is needed. Scripts fail on errors. The role script can update an existing explicitly named role;
the IAM-user flow uses a customer-managed policy and stops if that policy needs an explicit revision.

PassRole is limited to the supplied execution role ARNs and the ECS tasks or Bedrock AgentCore services.
Include AgentCore runtime execution role ARNs in that list for AgentCore automatic instrumentation. Optional Application
Signals setup permissions are excluded until enabled and are scoped to the supplied Lambda execution roles.
For IRSA, configure both Central backend and the connector service account; verification and reconciliation
run in different pods. The chart supports `cloudConnectors.aws.workloadIdentity` for this binding.

## Automatic and manual instrumentation

Bedrock AgentCore adds `aws.bedrock-agentcore-runtime`: automatic installation into existing Python 3.12 and CommonJS Node.js 22 artifacts,
ADOT/OpenLLMetry selection, automatic activation/rollback and read-only manual observation. This phase
requires a global `otlphttp` destination and excludes managed collector routing. See the
[AgentCore guide](../../../../docs/cloud-connectors/aws/workloads/agentcore/instrument.mdx) and
`odigos-apps/aws/bedrock-agentcore` for the 16-combination demo suite. Apply migration
`0024_connector_aws_agentcore.sql` before starting the updated connector (AWS schema version 13).

A workload capability with instrumentation enabled allows automatic mutation. Discovery without
instrumentation is manual mode: deploy the generated IaC yourself and Odigos observes it. Preserve unrelated
layers, variables, mounts, dependencies and containers. Never author `ODIGOS_MANAGED` by hand.

Lambda layers v0.0.19 or newer read version 2 `ODIGOS_LAMBDA_CONFIG`. Environment additions are compact
configuration plus the selected execution wrapper; attribution is recorded in Lambda tags. Web Adapter
configuration uses `webWrapper`, not the legacy short-key `w`. Default distros use one layer; ADOT uses SDK
and Collector layers. Lambda's five-layer and 4-KiB environment limits still apply. Automatic updates affect
`$LATEST`; deploy a new version and move aliases through your application pipeline. Alias and weighted
canary instrumentation are checked before claiming complete coverage.

Fargate services use one non-essential staging container and one shared volume. Each application mounts it
read-only and depends on staging `SUCCESS`. Overrides apply before language/version selection. Image-baked
loader values are preserved when available; environment-file loader values must first be made explicit.
Named overrides and manual reference instructions support multiple languages in one task. Windows,
standalone tasks, non-ECS deployment controllers and .NET musl images remain unsupported.

## Artifact sources

Build and release logic lives in `odigos-io/aws-connector-artifacts`. `ARTIFACTS_VERSION` pins the Lambda
bundle, Fargate agents image and npm CDK version together. Install `@odigos/aws-cdk` at that exact version.
The Python distribution is not published: use generated CloudFormation/Terraform instead of a PyPI command.

To build the provider, run `make build-connector-aws` from the enterprise repository root. To supply a local
Lambda bundle, use `make build-connector-aws LAMBDA_AGENTS_IMAGE=<registry>/odigos-lambda-agents:<tag>`.
Mirror the Fargate multi-architecture image without flattening its manifest and set the controller's
`CONNECTOR_AWS_FARGATE_AGENTS_IMAGE` override when required. This override affects every AWS connector
managed by that controller; ensure all task execution roles can pull it.

## Collectors and delivery

See [MANAGED-COLLECTOR.md](MANAGED-COLLECTOR.md) for the API workflow. The customer provides the ECS
cluster, execution role, host VPC and network connectivity. Automatic mode creates tagged regional
infrastructure; manual mode observes customer IaC and pushes configuration through OpAMP. Central must
publish a reachable TLS OpAMP endpoint and enable its gateway.

Routing checks inspect routes, peering, DNS support, security-group ingress/egress and subnet ACLs. Confirm
actual telemetry after deployment: custom DNS and other network appliances are outside this configuration
proof. Queues are ephemeral; restarts and Spot interruption can lose buffered telemetry. Collector-side TLS
termination is unavailable on AWS; supported load-balancer TLS uses customer-provided certificates and DNS.
