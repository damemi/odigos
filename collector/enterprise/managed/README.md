# Remotely managed enterprise Collector

The existing enterprise Collector image contains both `/odigosotelcol` and
`/odigosopampsupervisor`. The image still defaults to `/odigosotelcol`, so current Kubernetes and
on-prem deployments are unchanged. A cloud connector opts into managed mode with:

```yaml
command: ["/odigosopampsupervisor"]
args: ["--config=/etc/odigos/managed-collector/supervisor.yaml"]
```

## Deployment contract

The connector deployment must provide:

- `ODIGOS_OPAMP_ENDPOINT`: the Central OpAMP WebSocket or HTTP endpoint, normally `wss://...`.
- `ODIGOS_OPAMP_AUTHORIZATION`: the complete authorization header value, for example
  `Bearer <credential>`. **Long-lived and individually revocable, not short-lived**: this image sets
  `accepts_opamp_connection_settings: false`, so the header cannot be replaced over OpAMP and a new
  credential only arrives with a new deployment.
- `ODIGOS_CLOUD_CONNECTOR_ID`: the stable connector identifier used to associate the agent with its
  Central resource.
- `ODIGOS_CLOUD_PROVIDER`: `aws`, `gcp`, or `azure`.
- `ODIGOS_ONPREM_TOKEN`: the normal enterprise Collector authorization token. It is a signed JWT with
  an expiry, and `odigosenterpriseauthextension` refuses to start without a valid one and shuts the
  Collector down if it expires while running — which on a container platform is a crash loop that no
  configuration rollback can rescue. Renew the licence and redeploy before it expires.

  `ODIGOS_OPAMP_AUTHORIZATION` and `ODIGOS_ONPREM_TOKEN` must both be delivered from the platform's
  secret store as secret references, never as plain environment values, and from **two separate
  secrets** — they have different issuers, lifetimes and revocation authorities.
- `ODIGOS_COLLECTOR_BOOTSTRAP_CONFIG`: **optional.** A base64-encoded Collector configuration to serve on
  a cold start that cannot reach the control plane. See "Cold-start configuration" below. It carries the
  configuration's exporter credentials, so it is a **third** secret reference and never a plain
  environment value, for the same reason as the two above.
- A volume mounted at `/var/lib/odigos/collector-supervisor`, writable by UID/GID `65532`. Use
  persistent storage so the OpAMP instance identity, cached remote config, and last-known-good config
  survive rescheduling.

  On ephemeral storage none of that survives, and the two consequences are worth stating. A replaced
  task reports a **new OpAMP instance UID** — the identity that matters for routing is the credential,
  so routing is unaffected, but a control plane that keyed reporting on the instance UID would show one
  Collector as a stream of new ones. And a cold start has no cached last-known-good configuration, which
  is what `ODIGOS_COLLECTOR_BOOTSTRAP_CONFIG` exists for.
- Outbound TLS access to the OpAMP endpoint and to every destination configured in the remote
  pipeline. The image trusts the operating system CA bundle. For private CAs or mTLS, mount a custom
  Supervisor config and select it with `--config`.

The cloud workload should expose the Supervisor health endpoint on port `13134`. Port `13133` remains
available to the conventional Collector `health_check` extension if Central configures it. Telemetry
receiver ports are determined entirely by the remote Collector configuration.

## Remote configuration behavior

Central sends a complete OpenTelemetry Collector configuration via OpAMP. The Supervisor validates
it with `/odigosotelcol validate` before applying it. A successful change restarts the child
Collector; a startup failure automatically restores the persisted last-known-good remote config.
On first boot without Central or cached state, `fallback.yaml` starts an inert no-op Collector that
does not listen for or export customer telemetry.

## Cold-start configuration

`ODIGOS_COLLECTOR_BOOTSTRAP_CONFIG` closes the one gap the inert fallback leaves: a task replaced onto
fresh storage while Central is unreachable comes up accepting nothing, for as long as the outage lasts.
The control plane already generates the configuration it would push, so it can hand the same document
over at deploy time and the Collector comes up serving the last configuration that was published.

- The value is a **base64-encoded complete Collector configuration** — the same document Central would
  push over OpAMP, not a fragment. Base64 because the value crosses several platforms' environment
  plumbing on its way here, and a multi-line YAML document surviving all of them intact is an
  assumption rather than a guarantee. Surrounding whitespace and line wrapping are tolerated.
- The Supervisor writes it to `bootstrap-config.yaml` under `storage.directory`, mode `0600`, creating
  that directory if this is a first boot.
- `startup_fallback_configs` then becomes **the bootstrap document followed by the local files in
  `agent.config_files`** — `guardrails.yaml`. The bundled `fallback.yaml` is *replaced*, not merged:
  the Supervisor merges this list in order rather than trying its entries in turn, and collector confmap
  replaces lists on merge, so an inert entry after the real document would replace the real pipeline with
  `nop` and produce a Collector that starts, reports healthy and accepts nothing. Carrying the local
  files across is what keeps the enterprise authorization extension loaded, because the fallback path
  does not merge `agent.config_files`.
- The document is **never** a local config file. It is a fallback only, so a Collector that reaches
  Central runs the pushed configuration alone; one that merged the bootstrap into every push would serve
  a mixture of the two for ever.
- A document that cannot be decoded, written, or validated by `/odigosotelcol validate` is **ignored**
  and the inert fallback stands. This is a mitigation for an outage, and a mitigation that can stop a
  Collector from starting is worse than the outage. Every refusal is logged with the reason.
- Withdrawing the variable removes any document an earlier deployment left under `storage.directory`,
  so a persistent volume does not keep an exporter credential in a file nothing reads.

This image implements the managed agent side of the contract. The cloud-connectors control plane
must expose the authenticated OpAMP endpoint, associate the reported connector identity with the
customer resource, and generate a complete configuration using only components reported by that
Collector image.

`guardrails.yaml` is merged last. It keeps enterprise authorization enabled, while the Supervisor's
generated OpAMP extension keeps the Collector attached to the local Supervisor. Remote configuration
can change receivers, processors, exporters, connectors, pipelines, and Collector telemetry, but it
cannot update the Collector or Supervisor binaries. Image upgrades remain the responsibility of the
AWS/GCP/Azure deployment mechanism.

The image is published for `linux/amd64` and `linux/arm64` as one manifest list, to Artifact Registry,
Docker Hub and GHCR from a single build, with every ref signed against the same digest. Deployments
should reference it **by digest**, never by tag: the digest is the image's identity, and each cloud picks
whichever registry it can pull from without changing the bytes.

Remote configuration is trusted control-plane input. The cloud deployment must still minimize its
filesystem mounts, IAM permissions, Kubernetes RBAC, and network permissions because Collector
components execute with the permissions granted to the workload.

## Why this is the same image

Remote configuration can only reference components compiled into its Collector binary. Shipping the
Supervisor and Collector together guarantees that Central sees and configures exactly the enterprise
component set for that image version. A second image would duplicate the large distribution, create
version skew, and require a parallel release and security-patching lifecycle without adding isolation.
