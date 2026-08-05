# Cloud Connectors — Feature Context & PRD

> **Purpose:** Source-of-truth context for engineers and Cursor agents working on Cloud Connectors.
> Covers architecture across **ui-kit**, **odigos**, and **odigos-enterprise**, historical delivery,
> current state, and planned work — with emphasis on the **Azure connector** ([enterprise#3263](https://github.com/odigos-io/odigos-enterprise/pull/3263)).

---

## What are Cloud Connectors?

Cloud Connectors let **Odigos Central** manage telemetry for workloads and compute platforms that live
**outside** a connected Kubernetes cluster — starting with AWS, GCP, and Azure.

Each connector instance owns one **account boundary**:

| Provider | Boundary        | Example account field                  |
| -------- | --------------- | -------------------------------------- |
| AWS      | Account ID      | `123456789012`                         |
| GCP      | Project ID      | `my-gcp-project`                       |
| Azure    | Subscription ID | `0ad05b43-fc39-4362-8bf1-e8bcef5792d9` |

Connectors appear on the Central **Connections** page alongside K8s proxy clusters and VM agents
(`platformType = "connector"`). Users create them through a 3-step wizard in the Central UI.

### Two domains (per provider)

Every provider spec declares capabilities in two **separate** hierarchies:

1. **Workloads** — directly instrumentable resources (e.g. Lambda, Cloud Run, Container Apps).

- **Discovery** — enumerate candidates as Odigos Sources
- **Instrumentation** — attach agents / env in place (requires an OTLP destination)

1. **Compute platforms** — managed environments where Odigos would be _installed_ (EKS, GKE, AKS, EC2, VM).

- **Discovery** — detect platforms
- **Installation** — deploy odiglet / VM agent (mostly **not implemented yet**; discovery-only in v1)

The provider YAML (`connectors/spec/data/<provider>.yaml`) is the **single source of truth** for
permissions, auth methods, UI fields, setup artifacts, and capability types. Both the UI and backend
consume it — no duplicated wire contracts.

---

## Architecture

```mermaid
flowchart TB
  subgraph UI["ui-kit + central-ui"]
    Drawer["CreateCloudConnectorDrawer<br/>(3 steps)"]
    Table["CentralConnections table"]
  end

  subgraph Backend["central-backend (enterprise)"]
    GQL["GraphQL resolvers"]
    TC["testconnection.Run"]
    Svc["CloudConnectorService"]
    Spec["connectors/spec<br/>(embedded YAML)"]
  end

  subgraph K8s["odigos-central (Helm)"]
    CRD["OdigosCloudConnector CRD"]
    CRT["connector-runtime<br/>controller"]
    PG["connector-postgres"]
  end

  subgraph Runtime["Provider pod (enterprise)"]
    AWS["connector-aws"]
    GCP["connector-gcp"]
    AZ["connector-azure"]
    Base["connectors/base<br/>coordinator + central WS"]
  end

  Drawer -->|"createCloudConnector"| GQL
  Table -->|"cloudConnectorProviders"| GQL
  GQL --> Spec
  GQL --> TC
  GQL --> Svc
  Svc -->|"Secret + CR"| CRD
  CRT -->|"reconcile CR"| AWS
  CRT --> GCP
  CRT --> AZ
  AWS --> Base
  GCP --> Base
  AZ --> Base
  Base -->|"reverse WebSocket"| GQL
  CRT -->|"read heartbeat"| PG
  Base -->|"write heartbeat"| PG
```

### End-to-end create flow

1. **Step 1 — Select provider:** UI loads `cloudConnectorProviders` query → renders provider list,
   permission levels, custom capability toggles, and **policy preview** (IAM/RBAC templates from spec
   `setup.artifacts`).
2. **Step 2 — Setup credentials:** User picks auth method and fills credential fields.

- **Today (interim):** AWS still uses a hardcoded IAM wizard (`AwsCredentialsSections`); GCP/Azure use
  a generic renderer fed by spec fields ([damemi/ui-kit#1](https://github.com/damemi/ui-kit/pull/1)).
  ui-kit branches on `isAws` in `setup-credentials/index.tsx` — this is **not** the long-term design.
- **Target ([CORE-1389](https://linear.app/odigos/issue/CORE-1389/phase-2-spec-driven-create-cloud-connector-step-2-replace-isaws-fork)):**
  fully spec-driven Step 2 — auth methods, setup variants, create-role instructions, and credential
  fields all from `provider.auth.methods`, `connector.fields`, and `setup.artifacts` (see below).
- Credentials stay in browser memory only.

1. **Step 3 — Validate & create:** UI calls `testCloudConnectorConnection` then `createCloudConnector`.
2. **Backend validates in memory** via `connectors/testconnection` — identity, account match, discovery
   permissions. No Secret or pod yet.
3. **On success:** central-backend creates a K8s Secret (`<name>-credentials`) and upserts an
   `OdigosCloudConnector` CR.
4. **connector-runtime** (K8s controller) reconciles the CR → Deployment + Service for the provider pod.
   Stamps env vars from spec + Secret + `CONNECTOR_*_IMAGE` defaults.
5. **Provider pod starts:** verifies Postgres schema → registers heartbeat → runs discovery/reconcile
   loops → opens reverse WebSocket to central-backend.
6. **Status propagation:** controller reads `connector_platform.runtime_heartbeat` → updates CR
   conditions/phase → UI shows Connected / Error.

---

## Three-repo responsibilities

### ui-kit (`@odigos/ui-kit`)

**Role:** Presentation layer — drawer wizard, types, Storybook mocks. **No GraphQL/HTTP**; the host
app wires callbacks.

| Area                               | Key paths                                                |
| ---------------------------------- | -------------------------------------------------------- |
| Create drawer                      | `src/containers/_drawers/create-cloud-connector-drawer/` |
| Step 1 — provider + policy preview | `steps/select-provider/`                                 |
| Step 2 — credentials               | `steps/setup-credentials/`                               |
| Step 3 — validate                  | `steps/validate-connection/`                             |
| Form context                       | `src/contexts/cloud-connector-form-context.tsx`          |
| Types                              | `src/types/cloud-connectors/index.ts`                    |
| Connections table host             | `src/containers/central-connections/`                    |

**Host wiring (odigos-enterprise):**

- `central-ui/src/app/(v2)/(out-proxy)/connections/page.tsx`
- `central-ui/src/graphql/queries/cloud-connector-providers.ts`
- `central-ui/src/graphql/mutations/cloud-connector.ts`

**Release model:** ui-kit is published to npm; `central-ui` pins `@odigos/ui-kit` (currently `0.0.277`).

**Step 2 interim fork (to be removed by CORE-1389):**

```tsx
// ui-kit/src/containers/_drawers/create-cloud-connector-drawer/steps/setup-credentials/index.tsx
isAws ? <AwsCredentialsSections /> : <ProviderCredentialsSections />;
```

AWS sections 1–4 still use hardcoded `AUTH_METHODS`, `SETUP_METHODS`, and `IAM_ROLE_INSTRUCTIONS` in
`methods.ts`. GCP/Azure credential _fields_ come from spec, but the overall Step 2 layout is not yet
provider-agnostic.

---

### odigos (OSS repo)

**Role:** Kubernetes API + Helm packaging for Central infrastructure.

| Area                      | Key paths                                                                    |
| ------------------------- | ---------------------------------------------------------------------------- |
| CRD Go types              | `api/cloud-connectors/v1alpha1/`                                             |
| CRD YAML (Helm)           | `helm/odigos-central/templates/crds/odigoscloudconnectors.yaml`              |
| Feature gate + values     | `helm/odigos-central/values.yaml` → `cloudConnectors.*`                      |
| Connector infra templates | `helm/odigos-central/templates/connectors/`                                  |
| API docs                  | `docs/snippets/shared/api-reference/cloud-connectors.odigos.io.v1alpha1.mdx` |

**Helm gate:** `cloudConnectors.enabled: true` (default `false`) installs:

- `connector-runtime` Deployment (controller)
- Bundled PostgreSQL + PVC + migration Job
- `OdigosCloudConnector` CRD
- RBAC for central-backend to manage CRs/Secrets

**Provider image env vars** (stamped on `connector-runtime`, read by controller):

| Env var                 | Values key                    | Default image component             |
| ----------------------- | ----------------------------- | ----------------------------------- |
| `CONNECTOR_AWS_IMAGE`   | `cloudConnectors.aws.image`   | `odigos-enterprise-connector-aws`   |
| `CONNECTOR_GCP_IMAGE`   | `cloudConnectors.gcp.image`   | `odigos-enterprise-connector-gcp`   |
| `CONNECTOR_AZURE_IMAGE` | `cloudConnectors.azure.image` | `odigos-enterprise-connector-azure` |

Empty image overrides derive from global `imagePrefix` + `image.tag` via `utils.imageName` helper.

**Dependency direction:** odigos-enterprise imports CRD types from `github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1` (local `replace => ../odigos` during dev).

---

### odigos-enterprise

**Role:** All runtime binaries, shared connector library, Central backend GraphQL, provider implementations.

| Area                                        | Key paths                                              |
| ------------------------------------------- | ------------------------------------------------------ |
| Shared library (spec, testconnection, base) | `connectors/` — see [README](./README.md)              |
| Provider specs                              | `connectors/spec/data/{aws,gcp,azure}.yaml`            |
| Provider runtimes                           | `connectors/providers/{aws,gcp,azure}/`                |
| K8s controller                              | `connector-runtime/`                                   |
| Postgres schema                             | `connector-postgres/migrations/`                       |
| Central GraphQL                             | `central-backend/graph/cloud-connector.*`              |
| CR/Secret creation                          | `central-backend/internal/services/cloud_connector.go` |
| Example CRs                                 | `connectors/examples/`                                 |

**Build targets:** `make build-connector-{aws,gcp,azure,runtime,postgres}` (see root `Makefile`).

**Module graph:** `connectors/` is a Go module imported by central-backend, connector-runtime, and each
provider runtime via local `replace`.

---

## Cross-repo dependency summary

```
ui-kit  ──npm──►  central-ui  ──GraphQL──►  central-backend
                                                    │
                                                    ├── imports connectors/spec, testconnection
                                                    ├── creates OdigosCloudConnector CR (odigos API types)
                                                    │
odigos (Helm + CRD) ──installs──► connector-runtime ──creates──► provider Deployments
                                                    │
                                                    └── imports connectors/spec (env stamping)
```

**Rule of thumb for changes:**

| Change type                                 | Primary repo                                         |
| ------------------------------------------- | ---------------------------------------------------- |
| New provider field, permission, auth method | `odigos-enterprise/connectors/spec/data/`            |
| UI for new field / artifact type            | `ui-kit` (+ bump in `central-ui`)                    |
| Test-connection logic                       | `odigos-enterprise/connectors/testconnection/`       |
| Provider discovery/instrumentation          | `odigos-enterprise/connectors/providers/<provider>/` |
| CRD schema change                           | `odigos/api/cloud-connectors/` + Helm CRD template   |
| Install/infra (postgres, images, RBAC)      | `odigos/helm/odigos-central/`                        |
| GraphQL API shape                           | `odigos-enterprise/central-backend/graph/`           |

---

## Delivery timeline

### Phase 0 — Foundation (merged)

| Item                                          | Repo       | PR / ticket                                                                                                              |
| --------------------------------------------- | ---------- | ------------------------------------------------------------------------------------------------------------------------ |
| OdigosCloudConnector CRD + initial Helm infra | odigos     | [#5195](https://github.com/odigos-io/odigos/pull/5195) CORE-1199, [#5249](https://github.com/odigos-io/odigos/pull/5249) |
| connector-runtime controller + AWS connector  | enterprise | [#3069](https://github.com/odigos-io/odigos-enterprise/pull/3069)                                                        |
| Create connector on verified connection       | enterprise | [#3113](https://github.com/odigos-io/odigos-enterprise/pull/3113)                                                        |
| Central UI connections page + create dropdown | ui-kit     | [#1067](https://github.com/odigos-io/ui-kit/pull/1067) PLAT-1271                                                         |
| Credentials + validate steps                  | ui-kit     | [#1068](https://github.com/odigos-io/ui-kit/pull/1068), [#1069](https://github.com/odigos-io/ui-kit/pull/1069)           |
| Connections list polish                       | ui-kit     | [#1070](https://github.com/odigos-io/ui-kit/pull/1070)                                                                   |

### Phase 1 — GCP + policy preview + multi-provider UI (merged)

| Item                                                                                        | Repo       | PR / ticket                                                                                                             |
| ------------------------------------------------------------------------------------------- | ---------- | ----------------------------------------------------------------------------------------------------------------------- |
| GCP connector + Cloud Run instrumentation                                                   | enterprise | [#3179](https://github.com/odigos-io/odigos-enterprise/pull/3179) CORE-1277                                             |
| Connector-agnostic policy preview (backend)                                                 | enterprise | [#3273](https://github.com/odigos-io/odigos-enterprise/pull/3273) CORE-1346                                             |
| Connector-agnostic policy preview (UI)                                                      | ui-kit     | [#1119](https://github.com/odigos-io/ui-kit/pull/1119) CORE-1346                                                        |
| Provider-specific credentials Step 2 (**interim** — Azure/GCP unblock; AWS assume-role fix) | ui-kit     | [damemi/ui-kit#1](https://github.com/damemi/ui-kit/pull/1) → `@odigos/ui-kit@0.0.277` — **to be absorbed by CORE-1389** |
| Wire GCP/Azure connector images in Helm                                                     | odigos     | [#5409](https://github.com/odigos-io/odigos/pull/5409) CORE-1390                                                        |

### Phase 2 — Azure connector (**in progress**)

| Item                            | Repo       | PR / ticket                                                                 | Status                          |
| ------------------------------- | ---------- | --------------------------------------------------------------------------- | ------------------------------- |
| Azure cloud connector (backend) | enterprise | [#3263](https://github.com/odigos-io/odigos-enterprise/pull/3263) CORE-1342 | **Open** — last PR in the stack |

### Phase 3 — Future / backlog

| Item                                                                   | Ticket                                                                                                                      | Notes                                                                                                                                 |
| ---------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| **Spec-driven Step 2** (replace `isAws` fork, YAML-only new providers) | [CORE-1389](https://linear.app/odigos/issue/CORE-1389/phase-2-spec-driven-create-cloud-connector-step-2-replace-isaws-fork) | Child of [CORE-1345](https://linear.app/odigos/issue/CORE-1345); see [Setup wizard phases](#setup-wizard-phases-core-1345-epic) below |
| Compute platform **installation** (EKS, GKE, AKS, EC2, VM)             | —                                                                                                                           | Discovery exists; reconciler stubs return NotImplemented                                                                              |
| Azure Workload Identity auth path                                      | —                                                                                                                           | Spec declares `workload-identity` method; service-principal is the tested path                                                        |
| External Postgres for connector state                                  | —                                                                                                                           | Helm bundles postgres today; no external-DB values yet                                                                                |
| Connector CR `spec.runtime.image` per-environment overrides            | —                                                                                                                           | Supported on CR; Helm defaults cover most cases post-CORE-1390                                                                        |

---

## Setup wizard phases (CORE-1345 epic)

> **Note:** Linear uses "Phase 1 / Phase 2" for the **setup-wizard generalization** work
> ([CORE-1345](https://linear.app/odigos/issue/CORE-1345/generalize-cloud-connector-ui-data-sections)),
> which is separate from the **feature delivery** phases above (Foundation → GCP → Azure → backlog).
> Design doc: [Cloud Connector Setup (Google Doc)](https://docs.google.com/document/d/16P_JpQMyaL3hTnVejB8DjTTmlJNbQlJ7GGlQaheeHa0/edit).

### Setup Phase 1 — Step 1 policy preview ✅ (CORE-1346, merged)

Step 1 permission preview is spec-driven via `setup.artifacts` in provider YAML.

- ui-kit [#1119](https://github.com/odigos-io/ui-kit/pull/1119)
- enterprise [#3273](https://github.com/odigos-io/odigos-enterprise/pull/3273)

### Setup Phase 2 — Step 2 credentials 🔜 (CORE-1389, backlog)

**Goal:** Make Step 2 fully provider-agnostic so adding/extending a provider is YAML-only — no
`isAws` / per-provider React forks. Explicitly **out of scope** for CORE-1389: Azure backend
([CORE-1342](https://linear.app/odigos/issue/CORE-1342)) — that track is independent.

**Step 2 sections — today vs target:**

| Section                       | Today                                                      | Target (CORE-1389)                                                |
| ----------------------------- | ---------------------------------------------------------- | ----------------------------------------------------------------- |
| 1. Authentication method      | Hardcoded `AUTH_METHODS` (AWS) or spec (GCP/Azure interim) | `provider.auth.methods` for all providers                         |
| 2. Setup method               | Hardcoded `SETUP_METHODS` (AWS only)                       | `setup.artifacts` variants                                        |
| 3. Create role / instructions | Hardcoded `IAM_ROLE_INSTRUCTIONS` (AWS only)               | `setup.artifacts` with `steps` + template                         |
| 4. Enter credentials          | Partly via `getCredentialFields` inside AWS flow           | Always spec-fed from `connector.fields` + `auth.methods[].fields` |

**One-time model additions** (spec + GraphQL + ui-kit):

- Extend `SetupArtifact` with `variant` / `variantLabel` (setup-method chooser)
- Add `steps: []string` (ordered instructions above snippet)
- Scalar template vars alongside `{{permissions}}`: `{{accountId}}`, `{{externalId}}`, `{{projectId}}`, `{{subscriptionId}}`, etc.
- Generic renderer: `renderSetupArtifact(artifact, { permissions, vars })`

**Follow-through work:**

- Remove the interim `isAws ? … : …` fork; absorb [damemi/ui-kit#1](https://github.com/damemi/ui-kit/pull/1)
- Move AWS create-role content (CFN / Terraform / CLI / manual) from `methods.ts` into `aws.yaml` `setup.artifacts`
- GCP/Azure: auth + credential fields already in YAML; add create-role artifacts only where needed
  (Azure service-principal path may **omit** setup-method and create-role sections 2–3)
- Delete legacy client-side IAM policy fallback in `permissions.ts` once all backends publish artifacts

**Acceptance criteria (from ticket):**

- [ ] Step 2 has no provider-name branching (`aws` / `gcp` / `azure`)
- [ ] Auth method segment renders from `provider.auth.methods` for all providers
- [ ] Credential/account fields render from `connector.fields` + `auth.methods[].fields`
- [ ] Setup method + create-role UI only appear when the provider declares matching `setup.artifacts`
- [ ] Interim damemi/ui-kit#1 fork removed or absorbed
- [ ] Azure / GCP / AWS create-connector flows work in Storybook + Central

**Repos touched:** primarily **ui-kit**; model extensions also in **odigos-enterprise**
(`connectors/spec`, GraphQL schema) to expose new artifact fields.

---

## Azure connector (current focus)

### Open PR

**[odigos-enterprise#3263](https://github.com/odigos-io/odigos-enterprise/pull/3263)** — `core-1342-azure-cloud-connector` → `main`

All prerequisite PRs are merged. This PR adds Azure-specific backend only.

### v1 surface

| Domain            | Type                   | Discovery | Instrumentation / Installation                     |
| ----------------- | ---------------------- | --------- | -------------------------------------------------- |
| Workloads         | `azure.container-apps` | ✅        | ✅ Init container + EmptyDir + activation/OTLP env |
| Compute platforms | `azure.aks`            | ✅        | ❌ Installation disabled (stub reconciler)         |
| Compute platforms | `azure.vm`             | ✅        | ❌ Installation disabled (stub reconciler)         |

### What's in the PR

- **Spec:** `connectors/spec/data/azure.yaml`
  - Subscription boundary, workload-identity + service-principal auth
  - `setup.artifacts` policy preview → custom RBAC role JSON with `{{permissions}}`
- **Schema:** `connector-postgres/migrations/0008_connector_azure.sql` (GCP owns `0007`)
- **Test connection:** `connectors/testconnection/azure.go` — typed `*azcore.ResponseError` summarization
- **Runtime:** `connectors/providers/azure/`
  - Container Apps detector + inventory (ARM resource ID as stable identity)
  - Container Apps instrumentor: `Get` → mutate template → `BeginUpdate`
  - Java / Python / Node.js / .NET via OSS k8s-init-container pattern
  - `ODIGOS_MANAGED` ownership + rollback on disable
  - AKS + VM detectors (discovery only)
- **Wiring:** `CONNECTOR_AZURE_IMAGE`, `ODIGOS_AZURE_SUBSCRIPTION_ID`, `ODIGOS_CONTAINERAPPS_AGENT_IMAGE`
- **Docs:** `connectors/providers/azure/README.md`, `connectors/examples/azure-connector.yaml`
- **UI dep:** bumps `central-ui` to `@odigos/ui-kit@0.0.277`

### Azure-specific design notes

1. **Regional identity:** Container Apps are regional; inventory key is the full ARM resource ID
   (`/subscriptions/.../providers/Microsoft.App/containerApps/...`), not the short app name.
2. **Subscription boundary:** One connector = one subscription. Startup verifies boundary; rebind to a
   different subscription purges inventory.
3. **Instrumentation prerequisite:** Container Apps instrumentor waits for an OTLP destination (same
   pattern as GCP Cloud Run).
4. **RBAC for v1 testing:** Custom role "Odigos Cloud Connector" with:

- `Microsoft.Resources/subscriptions/read`
- `Microsoft.App/containerApps/read`
- `Microsoft.App/containerApps/write`
- `Microsoft.App/managedEnvironments/join/action` (required to PATCH apps in a Container Apps Environment)

1. **Service principal setup:** Portal IAM picker may not find SP by Client ID — use display name,
   Enterprise App Object ID, or CLI:

### Known issues encountered in testing

| Issue                                           | Root cause                                                                               | Fix                                                                       |
| ----------------------------------------------- | ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| Connector CR `ImageUnresolved` for Azure        | Helm only set `CONNECTOR_AWS_IMAGE`                                                      | [#5409](https://github.com/odigos-io/odigos/pull/5409) — merged           |
| Postgres PVC Pending                            | Cluster had no default StorageClass                                                      | Set default SC or `cloudConnectors.postgres.persistence.storageClassName` |
| Step 2 sent `{}` credentials for Azure          | ui-kit Step 2 was AWS-only (pre-interim fix)                                             | damemi/ui-kit#1 — merged; **proper fix is CORE-1389**                     |
| AWS assume-role over-validation                 | Required static keys when role ARN set                                                   | damemi/ui-kit#1 — merged; may move to spec-driven validation in CORE-1389 |
| Instrumentation 403 `LinkedAuthorizationFailed` | SP had `containerApps/write` but not `managedEnvironments/join/action` on the linked CAE | Add `Microsoft.App/managedEnvironments/join/action` to the custom role    |

---

## Local development & testing

### Oddish (recommended)

Oddish (`~/oddish`) automates build/push/deploy for dev clusters:

- **Central install:** `bin/k8s/apply/odigos.sh` → selects "Central", sets `imagePrefix`, `image.tag`, optional `--set cloudConnectors.enabled=true`
- **Build targets:** connector-runtime, connector-postgres, connector-{aws,gcp,azure} in `bin/common/tools/docker.sh`
- **No Oddish changes needed for CORE-1390** — Helm derives GCP/Azure images from global `imagePrefix` + `image.tag` once chart includes the env vars

**Required:** `ODIGOS_ONPREM_TOKEN` exported in shell.

### Kind / manual

See provider READMEs:

- `connectors/providers/aws/README.md`
- `connectors/providers/gcp/README.md`
- `connectors/providers/azure/README.md`

Example CRs: `connectors/examples/`.

### ui-kit local iteration

During UI work, `central-ui` can pin ui-kit via **yalc** (local link) instead of npm version.
Do not commit yalc pins — bump `@odigos/ui-kit` version for merge.

### Verify a healthy connector

```bash
# CR status
kubectl get odigoscloudconnector -n odigos-central
kubectl get odigoscloudconnector <name> -n odigos-central -o yaml

# Infra pods
kubectl get deploy,pods -n odigos-central | grep connector

# connector-runtime env (should list all three CONNECTOR_*_IMAGE after CORE-1390)
kubectl get deploy connector-runtime -n odigos-central -o yaml | grep CONNECTOR_

# Provider pod logs
kubectl logs -n odigos-central -l odigos.io/provider=azure --tail=50
```

Expected progression: `ImageUnresolved` → Deployment created → heartbeat `Starting` → `Connected`.

---

## Onboarding a new cloud provider

Checklist (mirrors AWS → GCP → Azure pattern):

1. **Spec** — add `connectors/spec/data/<provider>.yaml` (boundary, auth, workloads, computePlatforms, setup.artifacts)
2. **Tests** — `connectors/spec/<provider>_spec_test.go`
3. **Test connection** — `connectors/testconnection/<provider>.go`, register in registry
4. **Postgres migration** — `connector-postgres/migrations/000N_connector_<provider>.sql`, bump `ExpectedSchemaVersions`
5. **Provider runtime** — `connectors/providers/<provider>/` implementing `base.Provider`
6. **Dockerfile + Makefile targets** — `build/load/push/deploy/restart/port-forward-connector-<provider>`
7. **connector-runtime** — env constant in `internal/resources/resources.go`, image map in `cmd/main.go`
8. **Helm** — `CONNECTOR_<PROVIDER>_IMAGE` + `cloudConnectors.<provider>.image` in odigos chart
9. **ui-kit** — provider icon/mock data; Step 2 should eventually be fully spec-driven ([CORE-1389](https://linear.app/odigos/issue/CORE-1389)) — do not add new `isAws`-style forks
10. **Example CR + README**

---

## Key files quick reference

### Spec (edit first for any provider change)

```
connectors/spec/data/aws.yaml
connectors/spec/data/gcp.yaml
connectors/spec/data/azure.yaml
connectors/spec/model.go
connectors/spec/load.go
```

### Controller & infra

```
connector-runtime/internal/controller/odigoscloudconnector_controller.go
connector-runtime/internal/resources/resources.go
helm/odigos-central/templates/connectors/connector-runtime-deployment.yaml   # odigos repo
helm/odigos-central/values.yaml                                                # odigos repo
```

### Central backend

```
central-backend/graph/cloud-connector.graphqls
central-backend/graph/cloud-connector.resolvers.go
central-backend/internal/services/cloud_connector.go
central-backend/internal/k8s/cloud_connector.go
```

### UI

```
ui-kit/src/containers/_drawers/create-cloud-connector-drawer/
ui-kit/src/contexts/cloud-connector-form-context.tsx
central-ui/src/app/(v2)/(out-proxy)/connections/page.tsx
```

---

## Linear tickets

| Ticket    | Title                                              | Status                                                                                               |
| --------- | -------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| CORE-1199 | Initial connectors implementation                  | Done                                                                                                 |
| CORE-1277 | GCP cloud connector                                | Done                                                                                                 |
| CORE-1342 | Azure cloud connector                              | In PR [#3263](https://github.com/odigos-io/odigos-enterprise/pull/3263)                              |
| CORE-1345 | Generalize cloud-connector UI data sections (epic) | Todo — parent of CORE-1346 + CORE-1389                                                               |
| CORE-1346 | Setup Phase 1: policy preview (Step 1)             | Done                                                                                                 |
| CORE-1389 | Setup Phase 2: spec-driven Step 2 (`isAws` fork)   | Backlog — assigned Ben; branch `core-1389-phase-2-spec-driven-create-cloud-connector-step-2-replace` |
| CORE-1390 | Helm missing GCP/Azure connector image env vars    | Done [#5409](https://github.com/odigos-io/odigos/pull/5409)                                          |

---

## For Cursor agents picking up this work

1. **Read this file first**, then the relevant provider README and spec YAML.
2. **Spec changes drive everything** — start at `connectors/spec/data/`, run `go test ./connectors/spec/...`.
3. **Respect repo boundaries** — UI in ui-kit, CRD/Helm in odigos, runtime/backend in enterprise.
4. **The open Azure PR is enterprise-only** — [#3263](https://github.com/odigos-io/odigos-enterprise/pull/3263). UI and Helm prerequisites are already merged.
5. **Step 2 UI is interim** — do not treat `isAws ? AwsCredentialsSections : ProviderCredentialsSections` as the target architecture; follow [CORE-1389](https://linear.app/odigos/issue/CORE-1389) for Step 2 work (touches ui-kit + spec/GraphQL model extensions).
6. **Do not hand-edit generated files** (CRD codegen, GraphQL generated code).
7. **Testing a Central install** requires `ODIGOS_ONPREM_TOKEN` and `cloudConnectors.enabled=true`.
8. **Agent transcript** for the Azure debugging session: search agent transcripts for `CORE-1342`, `ImageUnresolved`, or cluster `ben-central-pro-plus`.

---

_Last updated: 2026-07-30 — aligned with [CORE-1389](https://linear.app/odigos/issue/CORE-1389) setup-wizard Phase 2 scope; merged ui-kit#1119, damemi/ui-kit#1, enterprise#3273, odigos#5409; open enterprise#3263._
