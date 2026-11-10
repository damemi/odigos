# connector-azure

The Azure provider connector runtime. It implements the provider-agnostic `base.Provider` seam
(`connectors/base`) for Microsoft Azure across two domains:

- **Workloads** (directly instrumentable via a Source)
  - `azure.container-apps` — Azure Container Apps. Discovery + instrumentation are implemented:
    an init container copies language agents into an EmptyDir at `/var/odigos`, and the app
    container receives activation + OTLP export env (OSS k8s-init-container pattern; eBPF is
    unavailable on Container Apps).
- **Compute platforms** (managed infra Odigos is installed onto, not instrumented directly)
  - `azure.aks` — Azure Kubernetes Service clusters (odiglet install target once installation lands).
  - `azure.vm` — Azure Virtual Machines (VM agent install target once installation lands).

## v1 surface

| Domain            | Type                   | Discovery | Action                                      |
| ----------------- | ---------------------- | --------- | ------------------------------------------- |
| Workloads         | `azure.container-apps` | ✅        | Instrumentation (init container + EmptyDir) |
| Compute platforms | `azure.aks`            | ✅        | Installation disabled                       |
| Compute platforms | `azure.vm`             | ✅        | Installation disabled                       |

One connector instance owns **one Azure subscription** (`ODIGOS_AZURE_SUBSCRIPTION_ID` /
`spec.account.id`). Startup verifies the subscription boundary and purges inventory when a connector
name is rebound to a different subscription.

Container Apps (and other Azure resources) are **regional**; the same display name can exist in
multiple regions/resource groups. Inventory identity is the full ARM resource ID
(`/subscriptions/.../resourceGroups/.../providers/Microsoft.App/containerApps/...`), which is the
Source `external_resource_id`. The short app name is only the OTel `service.name` candidate.

The base owns the discovery/reconcile loops, the agents-api server, the Central client, and
source/destination management. This module supplies the connector spec (`connectors/spec/data/azure.yaml`),
per-type discovery (detectors), and the (currently inert) reconciler/installation seams.

## Layout

```
cmd/main.go                          entry point: config -> base store -> base.Run(azureprovider)
pkg/config                           env config (subscription id, credentials) over base config
pkg/azureclient                      client factory + subscription boundary verification
pkg/azureprovider                    base.Provider implementation (wires detectors/reconcilers)
pkg/types                            spec type-name constants
pkg/workload/{detector,instrumentor,store}         Container Apps discovery + inventory (+ stub instrumentor)
pkg/computeplatform/{detector,reconciler,store}    AKS/VM discovery + inventory (+ stub reconcilers)
```

## Configuration (stamped by connector-runtime)

- `ODIGOS_AZURE_SUBSCRIPTION_ID` — the subscription boundary (`spec.account.id`). All API calls are
  scoped to it.
- `AZURE_TENANT_ID` / `AZURE_CLIENT_ID` / `AZURE_CLIENT_SECRET` — optional service principal fields
  (from the credentials Secret via the spec's `envVar` mapping). Empty uses
  `DefaultAzureCredential` (Azure Workload Identity / Managed Identity).
- Plus the generic base env (`ODIGOS_CONNECTOR_NAME`, `ODIGOS_DB_SCHEMA=connector_azure`,
  `ODIGOS_CAPABILITIES`, `DATABASE_URL`, `ODIGOS_CENTRAL_BACKEND_URL`, …).

## Auth methods

| Method              | Secret keys                            | Notes                                              |
| ------------------- | -------------------------------------- | -------------------------------------------------- |
| `workload-identity` | _(none)_                               | Pod ambient credentials via DefaultAzureCredential |
| `service-principal` | `tenantId`, `clientId`, `clientSecret` | Standard `AZURE_*` env vars                        |

## Required Azure RBAC

| Domain                           | Permissions                                                                                                              |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| Auth baseline                    | `Microsoft.Resources/subscriptions/read`                                                                                 |
| Container Apps (discovery)       | `Microsoft.App/containerApps/read`                                                                                       |
| Container Apps (instrumentation) | `Microsoft.App/containerApps/read`, `Microsoft.App/containerApps/write`, `Microsoft.App/managedEnvironments/join/action` |
| AKS (compute platform)           | `Microsoft.ContainerService/managedClusters/read`                                                                        |
| VM (compute platform)            | `Microsoft.Compute/virtualMachines/read`                                                                                 |

A custom role scoped to the target subscription should include the Actions above for the selected
access level. Discovery-only setups can omit `containerApps/write`; instrumentation requires it.

## Build

```
make build-connector-azure
make load-to-kind-connector-azure TAG=dev
make restart-connector-azure
# or: make deploy-connector-azure TAG=dev
```

## End-to-end workflow (kind)

1. Bootstrap the central stack once:
   ```
   make connector-dev-up TAG=dev ONPREM_TOKEN=<token> GITHUB_TOKEN=<pat>
   ```
2. Build + load the Azure connector image:
   ```
   make deploy-connector-azure TAG=dev GITHUB_TOKEN=<pat>
   ```
3. Create a credentials Secret (empty for workload identity, or service-principal keys) and apply
   [`connectors/examples/azure-connector.yaml`](../../examples/azure-connector.yaml) with your
   subscription id and image tag.
4. Verify pods and discovery:
   ```
   kubectl -n odigos-central get pods -l odigos.io/provider=azure
   make port-forward-connector-azure
   # inventory (example):
   kubectl -n odigos-central exec deploy/odigos-connector-postgres -- \
     psql -U odigos -d odigos -c "SELECT app_name, location, resource_id FROM connector_azure.container_apps WHERE deleted_at IS NULL;"
   ```

## Follow-up

- AKS odiglet install and VM-agent install (compute-platform installation feature).
- Optional image-config / buildpack language detection (source runtime override + heuristics land first).
- Gateway / managed-otelcol wiring.
