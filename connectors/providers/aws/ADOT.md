# AWS ADOT Lambda distributions

The Lambda SDK override accepts these names through GraphQL and the CDK `distro` option:

| Language | ADOT | ADOT + Application Signals | Pinned AWS SDK |
| --- | --- | --- | --- |
| Java | `java-lambda-adot` | `java-lambda-adot-application-signals` | 2.31.0 |
| Node.js | `nodejs-lambda-adot` | `nodejs-lambda-adot-application-signals` | 0.13.0 |
| Python | `python-lambda-adot` | `python-lambda-adot-application-signals` | 0.21.0 |

Existing upstream defaults are unchanged. Ruby has no ADOT distribution. .NET and custom runtimes remain outside this connector's Lambda runtime support. The catalog filters ADOT by runtime: Java 11/17/21, Python 3.10–3.13, and Node.js 18/20/22/24. Both architectures are packaged. These recipes support handler-mode functions; ADOT is not advertised for Lambda Web Adapter functions.

Each ADOT selection attaches two Odigos-owned layers: an AWS SDK layer and an architecture-specific collector layer. The collector is separate because combining it with the Java SDK exceeds Lambda's 50 MiB direct-upload limit. Switching SDKs removes the previous Odigos SDK/collector layers and preserves unrelated layers. Rollback restores managed environment variables and removes both attached versions. Two free layer slots are required.

The SDK explicitly exports each selected signal over OTLP/HTTP to the local collector, which forwards it to the selected destination. Disabled destination signals use `none`. Per-signal endpoints are required because AWS's Lambda configurators can otherwise redirect traces to X-Ray. All supported library instrumentations are enabled, including Node logging bridges and runtime instrumentation. Python uses pure-Python fallbacks for optional accelerators, avoiding the x86-only binaries in the published AWS zip on Graviton. Java uses the existing SDK-free invocation bridge with the AWS full agent, preserving Function URL/HTTP API context, stream handlers, and invocation flushing without initializing another SDK.

## Application Signals prerequisites

Plain ADOT explicitly disables Application Signals. The second preset enables it in addition to exporting selected signals to the Odigos destination. It requires destination traces to be enabled. AWS receives additional service telemetry through Lambda's embedded agent; this can incur AWS charges and is independent of ordinary OTLP application metrics/logs.

The native launcher routes auxiliary AWS metrics/log exporters to the local collector on HTTP port 4318. The Application Signals producer is enabled only when destination traces and metrics are both selected, because its independent metric reader ignores the ordinary metrics exporter switch. Other selections retain ADOT SDK instrumentation and export only their selected signals. These settings live in the layer rather than the function's environment configuration.

The pinned Java agent generates Application Signals RED metrics locally. The pinned Node.js and Python agents skip local RED-metric generation in Lambda and use AWS-side processing of enriched spans; their regular SDK metrics still export over OTLP. AWS-derived service metrics are not automatically forwarded to an arbitrary OTLP destination. Application logs require supported application logging calls (for example Pino, Python logging, or Logback); enabling a log pipeline does not create records in a function that never logs.

Before enabling this preset in an automatically managed connector, enable CloudWatch Application Signals service discovery in the account/region and grant the function's execution role `CloudWatchLambdaApplicationSignalsExecutionRolePolicy` (or equivalent permissions). The connector does not modify execution roles or account-wide service discovery. CDK adds the managed policy to the function role and emits the account setup prerequisite; imported roles must permit that policy change or have equivalent permissions already.

Terraform, CloudFormation, CLI and the structured plan include the chosen wrapper, compact settings, both layer ARNs and attribution tags. Account setup and execution-role permissions remain prerequisites for those manual methods too.

AWS setup: https://docs.aws.amazon.com/lambda/latest/dg/monitoring-application-signals.html

## Build and release

`aws-connector-artifacts` builds checksum-pinned SDKs plus the shared collector into the existing Lambda artifact image. Its release matrix publishes all layer families and generates the CDK catalog/SAR mappings. Release that repository, then bump the enterprise artifact/CDK version pins before shipping enterprise with these choices. For local image builds, override `LAMBDA_AGENTS_IMAGE` with the locally built `odigos-lambda-agents:adot-dev` image. No new agents-api enum or CRD field is required: distro overrides already carry catalog names as strings.

## Minimal Lambda configuration and rollback

Every supported Lambda distro (upstream Java/Node.js/Python/Ruby and ADOT) saves only `AWS_LAMBDA_EXEC_WRAPPER` and `ODIGOS_LAMBDA_CONFIG`. The latter is compact JSON with service name, signal mask, destination endpoint/transport/headers, optional debug/Application Signals flags, and the original execution wrapper for Web Adapter mode. Collector pipeline definitions and language defaults live in the layer. The native startup launcher expands SDK settings in the application process and generates collector configuration independently in the extension process. No runtime AWS API calls or extra execution-role permissions are needed to read this configuration.

Customer SDK/collector environment variables stay unchanged in AWS. The launcher applies Odigos settings only in its child processes. Web Adapter mode uses `/opt/odigos-webserver`, which loads the SDK and invokes the original adapter wrapper. ADOT remains handler-only. The connector checks the final UTF-8 environment size before updating AWS.

Ownership is recorded in Lambda resource tags: `odigos:managed` references a private immutable Postgres snapshot; `odigos:sdk-layer` and optional `odigos:collector-layer` identify exact attached versions. IaC deployments use `odigos:iac=v1` and the layer tags, without claiming connector rollback ownership. Tags never contain credentials or collector configuration. Discovery and observation read tags for every function, and automatic instrumentation requires `lambda:TagResource` and `lambda:UntagResource` in addition to `lambda:ListTags`.

A pending snapshot reference is tagged before an AWS update; the accepted reference is promoted only after a settled live read matches the pending configuration. This recovers safely from revision conflicts, asynchronous update failures, and a crash between configuration and tag updates. Snapshots are scoped to connector/function and retained independently of discovery pruning. Rollback preserves current customer environment values and restores original wrappers and attribution tags. Missing or invalid snapshots stop mutations. Legacy environment markers remain readable and are removed when migrating to tags. Retain database backups while managed functions exist; snapshot history is retained.

Build the Lambda artifacts and AWS connector images, passing the new artifacts image through `LAMBDA_AGENTS_IMAGE`. Also roll out the connector Postgres migration (AWS schema 11) and matching runtime controller/provider runtimes, which share strict schema checks. Apply migrations before starting updated runtimes. CDK users need the updated package. Rebuilding images does not change an already deployed function until the connector publishes/resolves the new layer and reconciles it. Older connector binaries do not understand tagged snapshot ownership; uninstrument with the updated connector before downgrading.
