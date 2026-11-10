# Connector instrumentation rules and regional-Collector actions: implementation plan

Status: plan, 2026-10-19. Working document, untracked like `providers/aws/MANAGED-COLLECTOR.md`.
Line numbers are from `9ed556d54` plus the uncommitted Lambda work in the tree.

1. What exists today
2. Decisions to confirm
3. Feature 1: instrumentation rules (global / region / source)
4. Feature 2: regional-Collector actions (custom processor, URL templatization, rename attribute)
5. Delivery order
6. Risks
7. Out of scope for v1

---

## 1. What exists today

### 1.1 Instrumentation rules

| | OSS Odigos (K8s) | vm-agent | Connector |
|---|---|---|---|
| Model | `InstrumentationRule` CRD, one-of by pointer field, 11 kinds (`odigos/api/odigos/v1alpha1/instrumentationrule_type.go:46`) | `BaseRule{name,type,enabled,notes}` + typed config, 4 kinds (`vm-agent/pkg/components/domain/rules/types/base.go:11`) | agents-api has the vm-agent kinds; every resolver is a stub (`base/gql/resolver.go:303-312`, `:468-477`) |
| Scope | `Scopes *SourcesScopes{Sources, Namespaces, Languages}`. nil = whole cluster, OR within a list, AND across lists (`odigos/k8sutils/pkg/scope/scope.go:50`) | None. Every rule is global, filtered by language at apply time. One exception: a `odigos-profiling-<svc>` / `odigos-symbols-<svc>` name prefix scopes custom probes (`controller/instrumentation/rule_helpers.go:157`) | None |
| Storage | CRs in the Odigos namespace | YAML in `rules.d/`, watched with fsnotify | — |
| Resolution | Per container: filter by scope, sort by (namespace, name), merge per kind (`instrumentor/controllers/agentenabled/sync.go:230-320`) into `InstrumentationConfig.Spec.Containers[]` | Merge all enabled rules (`core.go:550`), then a per-language gate (`workload.go:1669`) | — |
| Merge | Headers concatenated; code attributes OR; payload most-restrictive per field; custom probes appended; distro last-wins | Headers concatenated (deduped later in the SDK); code attributes OR; payload "any rule enables the category", sub-options dropped; probes unioned | — |
| Delivery | OpAMP `container_config`; env vars for `ConfigAsEnvVars` distros (`ODIGOS_AGENT_PAYLOAD_COLLECTION` etc., `pods_webhook.go:417-436`); eBPF `ConfigUpdate` | In-process eBPF SDK `ApplyConfig` | — |
| Central | Passthrough (`remoteFetch` → the cluster UI's GraphQL) | Passthrough over `/ws/proxy` | Passthrough over `/ws/proxy` |

Not to copy:
- OSS: updating a rule with an explicit null `sourcesScopes` widens it to global (`frontend/services/instrumentationrule.go:506-515`).
- OSS: a rule a distro cannot honour is ignored silently. Nothing tells the user.
- OSS: `InstrumentationLibraries` is read in one place (`signals/signals.go:50`); every merge ignores it.
- OSS: the mime-type merge builds its result from a Go map, so the order is nondeterministic.
- vm-agent: the payload merge discards sub-options; the rule name is used unsanitized as a file name; update is delete-then-create.

### 1.2 Actions

| | OSS Odigos | vm-agent | Connector |
|---|---|---|---|
| Model | `Action` CRD, one-of by pointer, 10 kinds, compiled into `Processor` CRs (`instrumentor/controllers/actions/action_controller.go`) | `BaseAction{name, actionName, notes, disabled, signals, pipelines, orderHint, type}`, 3 kinds: `urlTemplatization`, `processorAddition`, `spanMetrics` | agents-api has the vm-agent kinds; every resolver is a stub (`resolver.go:261-271`, `:416-427`) |
| Custom processor | `Processor` CRD with a raw `processorConfig`; no GraphQL | `processorAddition{type, processorName, processorConfig}` becomes `processors[type/processorName]` and is appended to matching pipelines (`controller/collector/actions_helpers.go:85`) | — |
| Rename attribute | `renameAttribute{renames map}` becomes a `transform` with `set`/`delete_key` per context per signal, order hint -50 (`instrumentor/controllers/actions/renameattribute.go:26`) | Does not exist; tests use a `processorAddition` of type `transform` | — |
| URL templatization | One shared `odigosurltemplate` processor, fed per workload through the `odigos_config_k8s` extension | `odigosurltemplate/vma-url-template` with `templatization_rules` (static mode, fixed name) | — |
| Validation | Webhook checks one-of and namespace only | Name, signals, type. No processor allow-list, no component check (`actions_profiles_test.go:51`) | — |

Not to copy:
- vm-agent's fixed processor name: two URL-templatization actions overwrite each other.
- vm-agent's `processorAddition` accepts any type and any config. A bad one fails otelcol's reload and crash-loops it.
- OSS rename builds OTTL with `fmt.Sprintf("set(attributes[\"%s\"], …")`. An attribute name containing `"` breaks the statement or injects another.
- OSS `DocumentedTemplates` appears never to reach the collector (`dynamicconfig/traces/urltemplatization.go:130` reads only `Templates`).

### 1.3 Connector facts that shape both features

- Central stores neither rules nor actions. The connector owns them, in connector Postgres.
- agents-api v0.28.0 has no scope construct anywhere. `region` is identity (Source, Destination, managed Collector), never a selector. Polymorphic input is a discriminator plus typed optional branches, and closed sets are `String!`, not enums.
- Connector workloads run upstream OpenTelemetry agents, configured only through environment variables.
  - Fargate uses the OSS community distros (`ecs_recipes.go:21`: java, python, dotnet, php and ruby community, plus a local upstream Node.js recipe).
  - Lambda uses the layers in `data/lambda_distros.json`. The layer expands one JSON env blob (`lambdaBootstrapConfig`, `lambda_config.go:20`) into SDK env at start.
  - There is no OpAMP and no eBPF channel, so **a rule has an effect only where the agent has an env setting for it.**
- Instrumentation is level-triggered: each pass diffs live state against desired state, with no input fingerprint. A new desired env var has to go into the injector, the diff, the plan and (manual mode) the observer. Miss one and already-instrumented workloads are judged "as desired" and never updated.
- There is one regional Collector per (connector, region), configured by OpAMP pull on heartbeat (~30 s).
  - There is one renderer: `awsprovider.DesiredConfig` (`providers/aws/pkg/awsprovider/collectors.go:73`) calls `collectorconfig.Render`. Render is pure, and the hash is the sha256 of the YAML.
  - Processor order is a tested contract: `memory_limiter, resourcedetection, resource(cloud.region), <destination processors>, batch` (`collectorconfig/config.go:259`, `:335`).
- The Collector image is the enterprise `odigosotelcol` (`collector/builder-config.yaml`). It includes `transform`, `attributes`, `filter`, `redaction`, `odigosurltemplate` and more.
  - Component availability is checked in tests only (`collectorconfig/distribution_test.go`).
  - `AvailableComponents` from OpAMP is parsed but unused (`base/opamp/session.go:248`).
- A config the Collector rejects (Supervisor `validate_config`) is held: `HoldRejected` never re-sends that hash.
- The next migration is `0022` (`connector-postgres/migrations`). Schema versions are platform 5, aws 11, gcp 3, azure 3 (`connectors/postgres/schema.go:19`).
- `odigos/common v1.29.0`, already in `base/go.mod`, has the `api/instrumentationrules` and `api/actions` types. It does not have `common/urltemplate`.

---

## 2. Decisions to confirm

The rest of this plan assumes the recommended option, listed first.

| # | Decision | Recommended | Alternative |
|---|---|---|---|
| D1 | How rules from different levels combine | **Additive**: every rule whose scope matches contributes, unioned or OR-ed per kind. Same as OSS and vm-agent, and all four agents-api kinds are additive. Cost: a source rule cannot switch off what a global rule switches on. | Most specific wins per kind (source, then region, then global) |
| D2 | Rule scope shape | Explicit `level` (`global` / `region` / `source`) plus `regions[]` or `sources[]`. Global is always chosen, never inferred from empty lists. An update without a scope keeps the current one. | OSS-style lists where empty means everything |
| D3 | Rule kinds a connector accepts | Only kinds at least one deployed agent supports (v1: HeadersCollection, CustomInstrumentation for Java). PayloadCollection and CodeAttributes are rejected at write time with a typed error. | Store them, and report "not applied" on every workload |
| D4 | How a rule change reaches automatic-mode workloads | On the normal reconcile pass, with a per-pass budget for drift that consists only of rule changes | Unthrottled. Editing a global rule then redeploys every Fargate service in one pass. |
| D5 | Manual-mode workloads | Rule env goes into the rendered instructions. Missing rule env is a non-error issue, not drift (`Partial`). | Treat it as drift. Every global rule edit then marks the whole manual fleet drifted. |
| D6 | Lambda delivery | A new field in the v2 config blob. Cross-repo: the aws-connector-artifacts layer. Land it before v2 ships. | Plain function env vars: no layer change, but separate snapshot, customer-edit and size handling |
| D7 | Action identity | Region-keyed `(region, name)`, like collector destinations. "All regions" is a UI fan-out. | A stored global scope |
| D8 | Action contract | Add an optional `region` to the existing generic `Action` types and operations (the Source/Destination precedent) | New `…ManagedCollectorAction` operations (the collector-destination precedent) |
| D9 | `processorAddition` types | Curated allow-list (§4.4) | Any processor compiled into the image |
| D10 | Several URL-templatization actions in one region | Merged into one `odigosurltemplate` instance. Correctness requires it (§4.3); it is listed here so it is visible. | — |

---

## 3. Feature 1: instrumentation rules

### 3.1 Semantics

- A rule applies to **enabled sources** only. Unmarked discovered workloads are never touched. This is the existing gate (`lambda.go:385`, `markBlockedOnPrerequisites`).
- Scope:
  - `global`: every enabled source of the connector.
  - `region`: every enabled source whose `Source.Region` is in `regions`.
  - `source`: every enabled source whose `(name, kind, region)` is in `sources`.
- The match key is the Source GraphQL identity `(name, kind, region)`. `Name` is the workload name, and does not change when the user edits the reported service name (`domain/source/source.go:103-111`).
- A `source` reference must name an existing source at write time, or the write fails with a typed error. If the source is deleted later, the reference stays and matches nothing. Recreating the source applies the rule again.
- Merge (D1, additive). Rules are sorted by name first.
  - HeadersCollection: union of keys, lowercased, deduped and sorted. The result becomes an env value, so an unstable order would read as drift on every pass.
  - CustomInstrumentation: union of probes, deduped, sorted by (class, method).
  - PayloadCollection and CodeAttributes: per-field OR, only if D3 changes.
- Rules with `enabled: false` are skipped. During drain, rules resolve to nothing.
- An ECS task can hold containers in several languages, so effective rule env is computed per container from that container's language.

### 3.2 What a rule can do: capability matrix

Step R0 confirms every cell against the pinned agent versions before code lands. The candidates:

| Rule kind | Java (OTel javaagent) | Python | .NET auto-instrumentation | Node.js | PHP, Ruby |
|---|---|---|---|---|---|
| HeadersCollection | `OTEL_INSTRUMENTATION_HTTP_SERVER_CAPTURE_REQUEST_HEADERS`, `…_SERVER_CAPTURE_RESPONSE_HEADERS`, `…_CLIENT_CAPTURE_REQUEST_HEADERS`, `…_CLIENT_CAPTURE_RESPONSE_HEADERS` | `OTEL_INSTRUMENTATION_HTTP_CAPTURE_HEADERS_SERVER_REQUEST`, `…_SERVER_RESPONSE` | `OTEL_DOTNET_AUTO_TRACES_ASPNETCORE_INSTRUMENTATION_CAPTURE_REQUEST_HEADERS`, `…_RESPONSE_HEADERS`, `OTEL_DOTNET_AUTO_TRACES_HTTP_INSTRUMENTATION_CAPTURE_REQUEST_HEADERS`, `…_RESPONSE_HEADERS` | No env setting: not supported | Verify; assume not supported |
| CustomInstrumentation (Java probes) | `OTEL_INSTRUMENTATION_METHODS_INCLUDE=pkg.Class[m1,m2];pkg.Other[m3]` | — | — | — | — |
| PayloadCollection | Odigos enterprise agents only (`ODIGOS_AGENT_PAYLOAD_COLLECTION`): not supported | | | | |
| CodeAttributes | Odigos enterprise agents only: not supported | | | | |

Notes:
- agents-api `headerKeys` has no direction. Odigos semantics capture the listed headers on both request and response, so the mapping sets both the server and the client keys wherever the agent has them.
- Lambda handler mode: the invocation span is not an HTTP server span, so R0 must check what header capture does there. It may only matter in web-server activation (Lambda Web Adapter).
- Lambda Java: `METHODS_INCLUDE` works only with the agent-based recipes, not the SDK wrapper handlers. Capability is therefore declared per recipe, not per language.
- Capability lives in data, next to each recipe:
  - Lambda: a `ruleSupport` field on each entry in `lambda_distros.json`.
  - Fargate: a connector-side table for the community recipes. The OSS community distro YAMLs don't declare these settings.
- The mapping from rule settings to env vars is provider-neutral pure logic in base. Upstream env names are the same on Lambda, ECS, Cloud Run and Container Apps.
- A rule that does not apply is always reported (§3.9), never ignored silently.

### 3.3 Contract: agents-api

New file `graphql/schema/rule/instrumentation_rule_scope.graphqls`:

```graphql
"""
Which workloads an instrumentation rule applies to. Cloud connectors only: the VM agent ignores it and
returns null, and every VM rule is global.
"""
type InstrumentationRuleScope {
  """global | region | source. A String rather than an enum, like every closed set in this schema."""
  level: String!
  """level = region: the regions whose sources the rule applies to."""
  regions: [String!]
  """level = source: the sources the rule applies to, by source identity."""
  sources: [SourceRef!]
}
type SourceRef { name: String!  kind: String!  region: String! }

input InstrumentationRuleScopeInput {
  level: String!
  regions: [String!]
  sources: [SourceRefInput!]
}
input SourceRefInput { name: String!  kind: String!  region: String! }
```

Changes to `rule/instrumentation_rule.graphqls`:
- Add `scope: InstrumentationRuleScope` to `interface InstrumentationRule` and to its four implementations (`CustomInstrumentationRule`, `PayloadCollectionRule`, `HeadersCollectionRule`, `CodeAttributesRule`).
- Add `scope: InstrumentationRuleScopeInput` to `CreateInstrumentationRuleInput`. On a connector, null means global, so existing clients keep working and create what they always created.
- Add `scope: InstrumentationRuleScopeInput` to `UpdateInstrumentationRuleInput`. Null or omitted keeps the current scope; widening to global requires `{level: "global"}`.
- genqlient sends every unset optional input object as `{}` (`domain/source/model.go:162-181`). A scope with an empty `level` counts as not provided.

Workload report (`workload/workload_report_v2.graphqls`):

```graphql
"""How one instrumentation rule resolved for this workload. Cloud connectors only."""
type WorkloadInstrumentationRule {
  name: String!
  ruleType: RuleType!
  """True when the rule's settings are part of this workload's desired instrumentation."""
  applied: Boolean!
  """Why it was not applied, written for a person, e.g. "Node.js agents have no setting for HTTP header capture"."""
  reason: String
}
# WorkloadDesiredReport gains:
#   instrumentationRules: [WorkloadInstrumentationRule!]
```

Then update `graphql/operations/{instrumentation_rule,workload_report}.graphql`, run `make generate`, and commit the generated Go and TypeScript.

vm-agent impact: on its next agents-api bump it must resolve the new `scope` fields, returning null.

### 3.4 Persistence

Migration `0022_connector_instrumentation_rules.sql`. The number depends on landing order with §4.6. Create the table in all three provider schemas, as sources were (`0002`, `0007`, `0008`):

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS connector_aws.instrumentation_rules (
  connector_name TEXT NOT NULL,
  name           TEXT NOT NULL,
  rule_type      TEXT NOT NULL,
  enabled        BOOLEAN NOT NULL DEFAULT true,
  notes          TEXT NOT NULL DEFAULT '',
  scope_level    TEXT NOT NULL DEFAULT 'global',
  scope          JSONB NOT NULL DEFAULT '{}',  -- {"regions":[...]} or {"sources":[{"name","kind","region"}]}
  config         JSONB NOT NULL DEFAULT '{}',  -- the one config branch that matches rule_type
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, name),
  CONSTRAINT instrumentation_rules_scope_level CHECK (scope_level IN ('global', 'region', 'source')),
  CONSTRAINT instrumentation_rules_name_not_blank CHECK (length(btrim(name)) > 0)
);
-- the same table in connector_gcp and connector_azure; bump schema_versions for aws, gcp, azure
-- +goose Down
-- DROP TABLE for each schema; restore the previous versions
```

- In `connectors/postgres/schema.go`, bump aws 11 → 12, gcp 3 → 4 and azure 3 → 4. `schema_test.go` enforces lockstep.
- Add `base/store/testdata/instrumentation_rules.sql` and a column test, as in `collectors_test.go`.
- `PurgeIntent` (`base/store/sources.go:288`) also deletes rules. An account rebind means fresh intent, and source-scoped rules would point at the old account's workloads.

### 3.5 Domain: `base/domain/instrumentationrule` (new, pure)

- `rule.go`:
  - `Rule{Name, Type, Enabled, Notes, Scope, Config}`
  - `Scope{Level, Regions []string, Sources []SourceRef}`
  - `Config` holds one of the `github.com/odigos-io/odigos/common/api/instrumentationrules` structs. They are already in `base/go.mod` at v1.29.0, and vm-agent uses the same types.
- `validate.go`:
  - exactly one config branch, matching `Type`
  - the kind is supported on connectors (D3)
  - scope: `region` needs at least one non-blank region; `source` needs at least one ref with all three fields; the field for the other level is absent; entries are deduped
  - CustomInstrumentation: `java` probes only, with class and method required. Reject `[`, `]`, `;` and `,` in names, since they delimit `METHODS_INCLUDE`.
  - HeadersCollection: at least one key, RFC 7230 token characters only. `,` is the env list separator.
  - name: `^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`
- `model.go`: `FromCreateInput`, `ApplyUpdateInput` (which keeps the scope when it is absent) and `ToModel` (which returns the matching concrete agents-api type). Follow `domain/source/model.go`, including the `{}` rule.
- `resolve.go`:
  ```go
  type WorkloadRef struct{ Name, Kind, Region string }

  type Effective struct {
      Headers      []string                               // merged, lowercased, sorted
      JavaMethods  []instrumentationrules.JavaCustomProbe // merged, sorted
      Contributing []string                               // matching rule names, sorted
  }

  func Resolve(rules []Rule, w WorkloadRef) Effective
  ```
- `env.go`, the capability matrix as data:
  ```go
  type AgentFamily string // "otel-java-agent", "otel-python", "otel-dotnet-auto", "otel-nodejs", ...

  type RuleEnv struct {
      Env      []plan.EnvVar // MergeAppend; Separator "," for headers, ";" for methods
      Statuses []Status      // per matching rule: applied, or why not
  }

  func EnvFor(e Effective, matching []Rule, family AgentFamily) RuleEnv
  func ManagedKeys(family AgentFamily) []string // closed set: the injector deletes any key not in Env
  func Catalog() []*model.InstrumentationRuleTypeOption
  ```
  The merge direction is append: the customer's own header list is kept and ours is added to it. On ECS, removing every rule restores the customer's original value, because append variables are rebuilt from the snapshot (`ecs_inject.go:133`).

### 3.6 Store, API, coordinator, GraphQL

- `base/store/instrumentationrules.go` adds `ListInstrumentationRules`, `GetInstrumentationRule`, `UpsertInstrumentationRule` and `DeleteInstrumentationRule`. It uses a whole-row upsert, since only customer mutations write.
- `base/api/central.go`: add rule CRUD to `CentralInterface` (`:346`).
- `base/coordinator/rules.go` (new):
  - Create and update: validate; for `source` scope, check each ref exists (the lookup in `coordinator.go:800-814`); upsert; then `triggerReconcile()`, as `UpdateConfig` does (`coordinator.go:178-193`).
  - Delete: delete, then `triggerReconcile()`.
  - `enrichWorkloads` resolves rules for each workload and fills a new `DiscoveredWorkload.Rules` field (`api/central.go:121`). Planners and reports then see the same result as the injectors.
- `base/gql/api/instrumentationrule.go` is a thin adapter. Replace the stubs in `resolver.go`: `InstrumentationRule`, `InstrumentationRules` and `InstrumentationRuleTypes` (`:303-312`), and the three mutations (`:468-477`). Errors are typed.
- Injectors and observers read `ListSources` themselves today (`lambda.go:435`, `fargate-task.go:130`). They read `ListInstrumentationRules` once per pass the same way and call `instrumentationrule.Resolve`. That gives one pure resolver with three call sites: coordinator enrichment, injector and observer.

### 3.7 AWS Fargate, automatic mode

In `providers/aws/pkg/workload/instrumentor`:

1. `containerTarget` gains `ruleEnv instrumentationrule.RuleEnv`. It is computed in `selectTargets` (`fargate-task.go:545`) from the container's language, mapped to its agent family.
2. `instrumentContainer` (`ecs_inject.go:128`) applies `ruleEnv.Env` through the existing append mechanism. It then deletes every key in `ManagedKeys(family)` that is not in `ruleEnv.Env`.
3. Add the managed keys to `ecsManagedEnvKeys` (`ecs_manifest.go:55-74`), so they are snapshotted and rolled back.
4. `containerDiff` (`ecs_inject.go:346`) and `alreadyInstrumented` (`:213`) compare rule env and report the component `instrumentationRules`.
5. `desiredFargateEnv` (`plan_fargate.go:106`) includes rule env for the planned language. `TestFargatePlanEnvMatchesInjector` keeps the injector and the plan in step.
6. Rollout budget (D4):
   - A rule-only diff, where every differing component is `instrumentationRules`, spends one unit of a per-pass budget.
   - With no budget left, the service is skipped this pass and reports the progress stage "Instrumentation rule update queued".
   - The config key is `instrumentationRules.rolloutBatchSize`, proposed default 5, added to the `config.Manager` defaults.

Also add `ComponentInstrumentationRules = "instrumentationRules"` to `base/api/observation.go`, with its label in `domain/workloadreport/model.go:647`. `TestEveryComponentHasALabel` enforces the label.

### 3.8 AWS Lambda, automatic mode

1. `lambdaBootstrapConfig` (`lambda_config.go:20`) gains `RuleEnv map[string]string` with JSON tag `ruleEnv,omitempty`. Add it to v2 while the v1 → v2 migration is still uncommitted, so it doesn't need a v3.
2. In aws-connector-artifacts, `lambda/config/main.go` exports the `ruleEnv` entries, combining them with the function's existing value in the direction the mapping specifies. This needs a layer release.
3. Ship the layer before the connector emits `ruleEnv`. In manual mode, check against the layer recorded in the IaC marker and report "layer too old" (`applied: false`).
4. `instrumentationDiff` (`lambda.go:832`) needs nothing extra: the blob is already diffed and hashed by `lambdaAppliedFingerprint` (`lambda_tags.go:16`). Keep the 4 KB environment check (`lambda_snapshots.go:61`) and report "rule settings too large for the Lambda environment" when it trips.
5. `desiredPrint` (`observe_lambda.go:387`) includes the effective rule env.
6. The same rollout budget as Fargate applies.

### 3.9 Reporting

- `WorkloadDesiredReport.instrumentationRules` is built from `Effective.Contributing` and `RuleEnv.Statuses`.
- When a matching rule cannot apply (language, recipe, layer or size), the workload gets the non-error issue `InstrumentationRuleNotApplied`. There is one issue per workload, and its details list the rules.
- `instrumentationRuleTypes` returns only kinds the connector supports, with `supportedLanguages` in the kit vocabulary (`nodejs` → `javascript`).

### 3.10 Manual mode (D5)

- Planner: `PlanFor` includes the rule env fragments, and `resolvePlaceholders` renders them.
  - The tool-agnostic `manual` document (`instrumentationplan`) gets a section, "Environment from instrumentation rules".
  - Tool-specific artifacts carry a note to add those variables.
  - Per-workload values are excluded from `plan.CoverageProblems`, which covers account-wide literals only.
- Observers (`observe_lambda.go`, `observe_fargate.go`) split the diff.
  - If the only missing component is `instrumentationRules`, the workload reports `Instrumented` plus the non-error issue `InstrumentationRulesNotApplied` ("re-apply your deployment to pick up rule changes").
  - It does not report `Partial`.

### 3.11 GCP and Azure

They use the same resolver.
- Add the env in `providers/gcp/pkg/workload/instrumentor/inject.go:66` and in the Azure `inject.go`.
- Extend `alreadyInstrumented` (`gcp inject.go:123`) and its Azure counterpart.
- Neither provider has observers, so there is no manual-mode work.

### 3.12 Central and UI

- central-ui rules UI on the agents-api path (`graphql/remote/vm-dialect.ts`), for `platformType=connector`:
  - a scope picker (Global, Regions, Sources)
  - regions taken from `sources` and `workloadReports`
  - a sources picker
  - `instrumentationRules` shown on each workload
- ui-kit: a scope form component.
- The connector nav (`utils/functions/navigation.ts:20-37`) shows only Overview and Collectors today. Add Rules.
- odictl (`vm-agent/cmd/odictl/runtime.go:48-66`) hides rules for connectors. Unhide them.

### 3.13 Tests

- Domain:
  - table tests for validation: each scope level, `{}` input, delimiter rejection
  - merge determinism: shuffled input gives the same output
  - the matrix for each agent family
  - the catalog
- Store: column test against the testdata SQL; upsert, list and delete; `PurgeIntent`.
- Coordinator:
  - create, update and delete trigger a reconcile
  - an unknown source ref is rejected
  - an update without a scope keeps the scope
- Fargate:
  - injector and plan stay in parity
  - a multi-language task gets per-container env
  - removing every rule restores the customer's value
  - the budget defers rule-only diffs and nothing else
- Lambda: blob golden file; layer gating; the fingerprint changes when rules change.
- e2e (oddish or kind, plus a test account):
  - a global headers rule on a Java Fargate service puts the header attributes on spans
  - a region rule leaves other regions alone
  - a source rule affects only that source

---

## 4. Feature 2: regional-Collector actions

### 4.1 Semantics

- An action belongs to one regional Collector, with identity `(region, name)` (D7). Like collector destinations, it requires that Collector to exist (`requireCollector`).
- It applies to everything that Collector receives. Workloads that export straight to the global destination (the routing fallback, `domain/destination/network.go:106`) bypass it, and the UI has to say so.
- Signals are TRACES, METRICS and LOGS. The managed Collector has no profiles pipelines.
- `pipelines` is rejected in v1. Pipeline names are internal; targeting a single destination is a follow-up.
- Pipeline order extends the existing contract:
  `memory_limiter, resourcedetection, resource, <actions sorted by (orderHint, name)>, <destination processors>, batch`.
  Actions run after Odigos's own enrichment and before anything destination-specific, so every destination sees the same result.
- Default `orderHint` per type: `renameAttribute` -50, `urlTemplatization` 1, `processorAddition` 0.
- `disabled` is stored and honoured: disabled actions are not rendered. Collector destinations have the same field, but `FromCreateInput` ignores it and nothing persists it. Fix that in the same pass, or at least don't copy the gap.

### 4.2 `processorAddition` (custom processor)

Input: `{type, processorName, processorConfig: JSON}`.

- The component id is `<type>/action-<name>`. The action name is unique per region and component-safe (`^[A-Za-z0-9_-]+$`, as for collector destinations), so two actions can never overwrite each other. `processorName` is stored and returned, but is only a label.
- `type` must be in the allow-list (D9, §4.4). Each allow-list entry declares the signals it supports, and the action's signals must be a subset of them.
- `processorConfig` must be a JSON object of at most 64 KiB.
- At render, deep-copy the config and run every string through `escapeExpansion` (`collectorconfig/destinations.go:316`). Otherwise `${env:…}` or `${file:…}` in a config value would make the Collector read its own environment, including credentials and the license, into telemetry.
- The Collector validates everything else (§4.9).

### 4.3 `urlTemplatization`

Input is unchanged from agents-api: `templatizationRulesGroups[{templatizationRules[{template, examples, notes}], notes}]`.

- **One processor per region.** All enabled URL-templatization actions in a region render into one processor, `odigosurltemplate/odigos-url-templatization`.
  - Its `templatization_rules` is the union of every template, deduped and sorted. Its hint is the lowest hint among the merged actions.
  - Why: the processor never overwrites `http.route` or `url.template` once set (`odigosurltemplateprocessor/processor.go:411-421`), and its default heuristics templatize every HTTP span. A second instance would never find a span left to apply its rules to. That is the vm-agent overwrite bug in another form.
- **Static mode applies to every HTTP span.** Without `odigos_config_extension`, the default heuristics (ids, UUIDs, dates, emails) apply to every HTTP span that passes through the Collector, not only spans that match a rule. OSS has the same default; say so in the UI.
- **Template syntax.** Validate templates at write time with `urltemplate.ParseUserInputRuleString`. That package is not in `odigos/common v1.29.0`. Either bump `odigos/common` to a version that has `common/urltemplate`, or do a minimal syntax check now and rely on the Collector's `Validate()`.
- The action is TRACES only.
- `examples` and `notes` are stored but not rendered.

### 4.4 `renameAttribute` (new action type) and the `processorAddition` allow-list

agents-api additions:

```graphql
enum ActionType { urlTemplatization processorAddition spanMetrics renameAttribute }

type AttributeRename { from: String!  to: String! }
type RenameAttributeConfig { renames: [AttributeRename!]! }
type RenameAttributeAction implements Action {
  name: String!  actionName: String  notes: String  disabled: Boolean!
  signals: [TelemetrySignal!]!  pipelines: [String!]  orderHint: Int  actionType: ActionType!
  region: String
  config: RenameAttributeConfig
}

input AttributeRenameInput { from: String!  to: String! }
input RenameAttributeConfigInput { renames: [AttributeRenameInput!]! }
# CreateActionInput and UpdateActionInput gain: renameAttribute: RenameAttributeConfigInput
```

The config is a list of pairs rather than OSS's JSON-string map, so it is typed and ordered.

It renders to `transform/action-<name>`:

```yaml
transform/action-<name>:
  error_mode: ignore
  trace_statements:            # only when TRACES is in signals
    - context: resource
      statements:
        - set(attributes["new"], attributes["old"]) where attributes["old"] != nil
        - delete_key(attributes, "old")
    - context: scope           # the same two statements
    - context: span            # the same
    - context: spanevent       # the same
  metric_statements:           # contexts resource, scope, datapoint; only when METRICS is in signals
  log_statements:              # contexts resource, scope, log; only when LOGS is in signals
```

- Attribute names go through a single OTTL string-quoting function, which escapes `\` and `"` and rejects control characters. Test it with hostile names.
- Validation:
  - at least one rename
  - `from` differs from `to`
  - each `from` appears once
  - no chains or cycles within an action (`a→b`, `b→c`), because statements run in order
- vm-agent impact: the new enum value reaches vm-agent on its next agents-api bump. It must either reject `renameAttribute` or implement it with the same rendering. Its coverage tests will force that choice.

`processorAddition` allow-list for v1. Each entry's signals are confirmed from the component's factory in A2; this table is the starting point.

| Allowed | Signals |
|---|---|
| `attributes`, `filter`, `transform`, `resource`, `groupbyattrs` | traces, metrics, logs |
| `span` | traces |
| `redaction` | traces; check metrics and logs at v0.159.0 |
| `probabilistic_sampler` | traces, logs |
| `metricstransform`, `cumulativetodelta`, `deltatorate`, `metricsgeneration` | metrics |
| `odigosextractattribute` | traces (verify) |

| Excluded | Why |
|---|---|
| `memory_limiter`, `batch`, `resourcedetection` | Owned by Odigos. Their position and settings are the pipeline contract. |
| `tail_sampling`, `odigostailsampling`, `groupbytrace` | They need every span of a trace on one replica, but the regional Collector scales out behind a load balancer. |
| `k8sattributes` | No Kubernetes API on ECS |
| `remotetap` | Opens a listener |
| `odigospiimasking`, `odigossqlquery`, `odigossqldboperationprocessor`, `odigosconditionalattributes`, `odigostrafficmetrics`, `odigos_trace_filter`, the profiles processors | They expect the Odigos config extension or are internal. Revisit each one. |
| `odigosurltemplate` | Use the `urlTemplatization` action; a second instance cannot work (§4.3). |
| `sumologic` | Vendor-specific. Add it on request. |

Add a test to `distribution_test.go` that asserts every allowed type is compiled into `collector/builder-config.yaml`.

### 4.5 Contract: agents-api

In `graphql/schema/action/action.graphqls` (D8):
- Add `region: String` to `interface Action` and every implementation. Description: "Cloud connectors only: the regional Collector this action configures. The VM agent ignores it."
- Add `region: String` to `CreateActionInput`, `UpdateActionInput` and `DeleteActionInput`.
- Change the queries to `action(name: String!, region: String): Action` and `actions(region: String): [Action!]!`.
- Add `RenameAttributeAction` and its inputs (§4.4).
- Optionally add `actionName: String` to `ManagedCollectorConfigIssueDetails` (`connector/managed_collector.graphqls:334`), so a rejection can name the action responsible.

On the connector, `region` is required on writes, and `actions(region: null)` lists every region.

### 4.6 Persistence

Migration `0023_connector_aws_collector_actions.sql`. AWS only, like `0016` collector destinations; GCP and Azure have no Collector.

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS connector_aws.collector_actions (
  connector_name TEXT NOT NULL,
  region         TEXT NOT NULL,
  name           TEXT NOT NULL,
  action_type    TEXT NOT NULL,
  action_name    TEXT NOT NULL DEFAULT '',
  notes          TEXT NOT NULL DEFAULT '',
  disabled       BOOLEAN NOT NULL DEFAULT false,
  signals        JSONB NOT NULL DEFAULT '[]',
  order_hint     INTEGER,                     -- NULL: the type's default
  config         JSONB NOT NULL DEFAULT '{}',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (connector_name, region, name),
  CONSTRAINT collector_actions_region_not_blank CHECK (length(btrim(region)) > 0),
  CONSTRAINT collector_actions_name_component_safe CHECK (name ~ '^[A-Za-z0-9_-]+$')
);
-- bump connector_aws in schema_versions
-- +goose Down
-- DROP TABLE; restore the previous version
```

Bump the aws version: 12 → 13 if the rules migration lands first, otherwise 11 → 12. Add a test copy in `base/store/testdata/collector_actions.sql`.

### 4.7 Domain and render

`base/domain/collectoraction` (new, pure):
- the model
- `Validate`: allow-list, signals, names, renames, templates, and config object and size
- `FromCreateInput`, `ApplyUpdateInput` and `ToModel`, which returns one of `ProcessorAdditionAction`, `URLTemplatizationAction` or `RenameAttributeAction`
- the allow-list table
- the catalog for `actionTypes`

`base/domain/collectorconfig`:
- Add `Input.Actions []collectoraction.Action` (`config.go:57`).
- New `actions.go` with `renderActions(in) (processors map[string]any, perSignal map[string][]string, problems)`. It:
  - merges URL templatization into one processor
  - builds the `transform` for renames
  - copies and escapes `processorAddition` configs
  - sorts the per-signal name lists by (hint, name)
  - drops an action that fails to render with a `Degraded` problem, as it does for a destination
- `renderPipelines` (`:300`) inserts each signal's action names after `own[:len(own)-1]` and before the destination processors. It does this in entry pipelines and in the no-destination `nop` pipelines. Batch stays last.
- `mergeComponents` / `merge`: a component-id clash is a render error.
- Tests:
  - golden files: `action_processor_addition.yaml`, `action_rename_attribute.yaml`, `action_url_templatization_merged.yaml`, `actions_ordering.yaml`, `actions_signals_filter.yaml`, `actions_no_destination.yaml`, `action_disabled.yaml`
  - update `TestProcessorOrderIsTheContract` (`config_test.go:556`) for per-signal processor sets
  - add action inputs to `TestRenderedDocumentNamesNothingUnexpected`
  - an escape test: `${env:AWS_SECRET_ACCESS_KEY}` in a config value renders as `$${env:AWS_SECRET_ACCESS_KEY}`

### 4.8 Store, coordinator, API, GraphQL, provider

- `base/store/collectoractions.go`, shaped like `collectordestinations.go`: `Upsert`, `Get`, `List(region)`, `ListAll`, `Delete`, `DeleteForRegion`.
- `base/api/central.go`: action methods on `CentralInterface`.
- `base/coordinator/collectorapi.go`: `Create`, `Update`, `Delete`, `Get` and `ListCollectorAction`.
  - The write path is: validate, `requireCollector(region)`, duplicate check, upsert, then `TriggerCollectorReconcile()`.
  - The trigger refreshes the cold-start bootstrap secret. Running tasks pick up the change on the next OpAMP heartbeat, about 30 s.
- Teardown: add `DeleteCollectorActionsForRegion` to the `collectorResourceStore` interface (`collectors.go:37-48`) and call it in `removeCollectorLocalState` (`collectorapi.go:854`).
- `base/gql/api/action.go` is the adapter.
  - Replace the stubs `ActionTypes`, `Action` and `Actions` (`resolver.go:261-271`), and `CreateAction`, `UpdateAction` and `DeleteAction` (`:416-427`).
  - `spanMetrics` returns a typed "not supported on cloud connectors" error.
- `DesiredConfig` in `providers/aws/pkg/awsprovider/collectors.go:73` calls `p.bstore.ListCollectorActions(ctx, key.Region)` and passes the result as `Input.Actions`. It stays the single render site, so the OpAMP push and the bootstrap secret stay identical.

### 4.9 When the Collector rejects a config

Today the Supervisor validates the config and reports FAILED. The connector then:
- holds that hash (`HoldRejected`, `base/opamp/session.go:205`)
- sets the status to `Degraded`
- never sends the same hash again

With actions, one bad custom processor would block every later change in that region, a new destination included, until the action is fixed or removed.

Plan:
1. Attribution (A3, required). The rejection text already reaches `ManagedCollectorConfigIssueDetails`. Parse the component id (`<type>/action-<name>`) out of it and fill `actionName`, so the UI can point at the action.
2. Quarantine (A5, recommended).
   - Store `rejected_config_hash` on the Collector.
   - When the held hash's rejection names an action, render without that action and raise a `Degraded` problem: "action X was rejected by the Collector: …".
   - Editing the action clears the quarantine, and other changes keep flowing meanwhile.
3. Surface render `Degraded` problems in the collector report. Today `DesiredConfig` only logs them (`collectors.go:110`).

### 4.10 Central and UI

- central-ui `graphql/remote/vm-actions.ts` today drops `processorAddition` and `spanMetrics` on read and throws on create for anything except URL templatization (`:19-28`, `:62-70`, `:106-123`). Add a connector branch with:
  - processorAddition: a type select fed by `actionTypes`, and a JSON editor
  - renameAttribute: a pairs editor
  - urlTemplatization
  - `region` taken from the Collector page
- Put Actions on the regional Collector page, next to its destinations. "Apply to all regions" fans out one call per region.
- ui-kit: a rename-pairs field, and a JSON-object field with validation.

### 4.11 Tests

- Domain: table tests for validation and compilation; OTTL quoting with hostile names; the escape test.
- Render: the golden files (§4.7) and the allow-list distribution test.
- Store: column test.
- Coordinator: a write triggers a reconcile; writes require the Collector; teardown deletes actions.
- gql: round trip, including `{}` config branches.
- e2e: a Collector in a test account with a debug destination.
  - rename `http.method` to `http.request.method`
  - URL templatization `/users/{id}`
  - a `processorAddition` of type `attributes` that inserts an attribute
  - a deliberately bad config: the issue names the action, and once A5 lands, the action is quarantined

---

## 5. Delivery order

Actions only change Collector config, not customer workloads, so they carry less risk; land them first unless there's a reason not to. The two tracks share only the agents-api release and the migration numbers.

| # | Repo | Change | Needs |
|---|---|---|---|
| C1 | agents-api | Action `region`, `renameAttribute`, `actionName` on config issue details | — |
| C2 | agents-api | Rule `scope`, `WorkloadInstrumentationRule` | — (same release as C1) |
| — | agents-api | Release v0.29.0. Before the release, consume the branch with `go get github.com/odigos-io/agents-api/go/server@<branch>` in `base` and in every `providers/*` module. | C1, C2 |
| A1 | enterprise | Migration `collector_actions` and schema bump | — |
| A2 | connectors | `collectoraction` domain, `collectorconfig` render, tests | C1 branch |
| A3 | connectors | Store, coordinator, teardown, gql, `DesiredConfig`, rejection attribution; bump `connectorGraphQLSchemaVersion` (`base/run.go:33`) | A1, A2 |
| A4 | central-ui, ui-kit | Actions on the Collector page | release |
| A5 | connectors | Quarantine and surfaced render problems | A3 |
| R0 | — | Spike: confirm the capability matrix on the pinned agent versions, including Lambda handler mode | — |
| R1 | enterprise | Migration `instrumentation_rules` and schema bumps | — |
| R2 | connectors | `instrumentationrule` domain: model, validate, resolve, env, catalog | C2 branch, R0 |
| R3 | connectors | Store, coordinator CRUD, enrichment, gql, reports, `PurgeIntent` | R1, R2 |
| R4 | connectors | AWS Fargate automatic mode and the rollout budget | R3 |
| R5 | aws-connector-artifacts, then connectors | Lambda `ruleEnv` blob field, layer release, gating | R3; before the v2 blob ships |
| R6 | connectors | Manual mode: instructions and the observer split | R4, R5 |
| R7 | connectors | GCP and Azure | R3 |
| R8 | central-ui, ui-kit, vm-agent (odictl) | Scope UI, per-workload rule status, odictl unhide | release |

Migration numbering: whichever of A1 and R1 merges first takes `0022`. The other renumbers to `0023` and takes the next aws version.

---

## 6. Risks

- **Fleet-wide redeploys.** A global rule edit changes desired state for every source. Fargate re-registers task definitions and rolls services, and Lambda publishes new configurations. The budget (D4) bounds this; agree its default with the team.
- **Secrets in captured headers.** Header capture can copy `authorization` or `cookie` into telemetry. Decide between a UI warning and rejecting those keys unless explicitly confirmed.
- **Arbitrary logic on the telemetry path.** `processorAddition` lets an admin add any allowed processor; mutations already require `is_admin`. Escaping stops a config from reading the Collector's environment, and the allow-list keeps out components that break a gateway.
- **URL templatization changes routes.** In static mode it rewrites `http.route` on every HTTP span in the region, so dashboards keyed on raw paths will change.
- **Actions can be bypassed.** They don't apply to workloads that route straight to the global destination.
- **Cross-repo Lambda change.** The layer change is versioned and lives in another repo. Old layers ignore `ruleEnv`, which is reported, not silent.
- **`odigos/common` bump.** Bumping it for `urltemplate` may bring other changes into `base`; review that diff.

## 7. Out of scope for v1

- Scoping actions by source or service. This could come later with OTTL `where resource.attributes["service.name"] == …`.
- `spanMetrics` on the regional Collector.
- `pipelines` targeting, and per-destination actions.
- Rule kinds beyond the four in agents-api (distro override, trace config, sampling).
- A language filter on rule scope (OSS `Languages`). The capability matrix handles per-workload language instead.
- Odigos enterprise agents on connector workloads. They would enable PayloadCollection and CodeAttributes through the `ODIGOS_AGENT_*` env vars.
