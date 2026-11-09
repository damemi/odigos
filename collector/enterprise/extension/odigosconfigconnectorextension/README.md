# Managed regional Collector configuration

`odigos_config_connector` provides action settings to processors implementing the shared
`OdigosConfigExtension` contract. Its configuration is delivered in the complete Collector
configuration through OpAMP. It does not read files, poll a cloud API, or depend on the VM extension.

```yaml
extensions:
  odigos_config_connector/managed-actions:
    # Applies to every resource, including unknown workloads and languages.
    collector_config:
      dbQueryTemplatization:
        templatizeLiterals: true
    workloads:
      - id: checkout
        resource_attributes:
          cloud.provider: aws
          cloud.region: us-east-1
          service.name: checkout
      - id: payments
        resource_attributes:
          cloud.provider: aws
          cloud.region: us-east-1
          service.name: payments
    scopes:
      # Applies to either listed workload, regardless of SDK language.
      - workload_ids: [checkout, payments]
        collector_config:
          inferDbAttributes: {}
      # Applies to all Java resources, including workloads not listed above.
      - languages: [java]
        collector_config:
          dbQueryTemplatization:
            removePostgresCastOperator: true
      # Applies only to Java resources belonging to checkout.
      - workload_ids: [checkout]
        languages: [java]
        collector_config:
          urlTemplatization:
            templatizationRules: ["/orders/{id}"]
            default: {}

processors:
  odigossqlquery/managed-actions:
    odigos_config_extension: odigos_config_connector/managed-actions
  odigosurltemplate/managed-actions:
    odigos_config_extension: odigos_config_connector/managed-actions

service:
  extensions: [odigos_config_connector/managed-actions]
  # Include the processors above in the relevant traces pipelines.
```

## Matching and merging

- Workload selectors require exact string matches for **all** listed resource attributes. Use
  stable, distinguishing attributes actually emitted by instrumentation; display/service names
  alone are not necessarily unique across clusters or accounts. There is no inference from source
  names or live AWS lookups. Ambiguous/overlapping selectors and unknown workload IDs are rejected.
- Language matching uses the resource attribute `telemetry.sdk.language`, with exact lowercase
  SDK values such as `java`, `python`, or `dotnet`. Missing, non-string, or different values do not
  match a language filter. This selects spans by their resource, not instrumentation scope name.
- Values within `workload_ids` or `languages` are ORed. When both are present, they are ANDed.
  A scope must specify at least one filter; the top-level `collector_config` is the global scope.
- Global settings and **every** matching scope are combined. SQL flags are ORed; DB inference is
  enabled by presence; URL templates are unioned/deduplicated and defaults use the same shared
  merge semantics as VM/Kubernetes actions (disabled-default wins). Scoped false values do not
  disable globally enabled actions. Scope order does not establish precedence.
- The extension currently serves URL templatization, DB query templatization, and DB inference.
  PII masking remains excluded. Extract/Delete/Rename and processor-addition actions are separate
  pipeline processors; this extension does not make those processors workload/language-aware.

Configuration and processor cache keys are precomputed from configured workloads and language
filters. New task IDs, instance IDs, or unrecognized incoming languages cannot grow the cache.
Callbacks receive all precomputed configurations on registration, including global-only fallback.
No workload or language is ever dropped for not matching a scope. An empty extension is valid and
provides empty action settings. Changes take effect when the OpAMP supervisor applies the next
Collector configuration; no separate file watcher or remote control connection is needed.

The connector currently renders collector-wide UI actions into `collector_config`. The scoped
configuration contract is available here; source/language selection in the UI is not yet exposed.
