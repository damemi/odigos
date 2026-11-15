# Odigos patches on the vendored service graph connector

This package is copied from [opentelemetry-collector-contrib](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/connector/servicegraphconnector) (module path unchanged). These notes list **Odigos-only behavior** so you can re-apply it after upgrading the vendored tree.

---

## 1. `upsertPeerAttributes` — no `break` (fill the whole `Peer` map)

| Item | Detail |
|------|--------|
| **Where** | `connector.go` — `upsertPeerAttributes` |
| **Upstream** | After the first `VirtualNodePeerAttributes` entry found on the span, the loop **`break`s**, so `e.Peer` holds at most **one** key. |
| **Odigos** | **`break` removed** — every attribute name in the config list that exists on the span is written into `peers` (same loop, no early exit). |
| **Why** | `getPeerHost` still uses **priority order** only for the primary `server` label. Odigos also needs **all** present peer attributes in `e.Peer` for §2–§3 (aggregation key + extra `server_*` metric labels). |

---

## 2. `buildMetricKeyFromEdge` (internal series key)

| Item | Detail |
|------|--------|
| **Where** | `connector.go` — `aggregateMetricsForEdge` calls `buildMetricKeyFromEdge(e)` instead of `buildMetricKey(..., e.Dimensions)`. |
| **Why** | The `server` label uses the **first** matching `virtual_node_peer_attributes` value (`getPeerHost`). Other peer fields can differ while `server` stays the same; the key must include them or series merge incorrectly. |
| **What it does** | Starts from `buildMetricKey`. For `virtual_node` **or `database`** edges with non-empty `e.Peer`, appends sorted `peer\|<key>\|<value>`. Skips keys already in `e.Dimensions` as `client_*` / `server_*`. |

---

## 3. `buildDimensions` peer labels + `sortedMapKeys`

| Item | Detail |
|------|--------|
| **Where** | `connector.go` — `buildDimensions` (virtual-node / database branch) and `sortedMapKeys` |
| **Why** | Odigos UI shows peer details on virtual-node and database nodes (e.g. `peer_db.system`); the `peer_` prefix keeps them out of the `server_*` dimensions that identify a node. Upstream only copies `e.Dimensions` onto the datapoint. Database edges are completed immediately during span processing (before `onExpire`), so they must be handled alongside virtual-node edges. |
| **What it does** | For `virtual_node` **or `database`** edges with non-empty `e.Peer`, adds `peer_<peerKey>` unless `server_<peerKey>` is already in `e.Dimensions`. Uses **sorted** peer keys (same order as §2). |

---

## 4. Database node naming

| Item | Detail |
|------|--------|
| **Where** | `connector.go` — client span handling, database branch |
| **Odigos** | `server` is the first `virtual_node_peer_attributes` value on the span, falling back to the `database_name_attributes` value (upstream uses only the latter). |
| **Why** | Clients set `db.name` inconsistently, so one database got different names. |

---

## 5. In-cluster peer namespace (`resolveInClusterPeer`)

| Item | Detail |
|------|--------|
| **Where** | `connector.go` — `resolveInClusterPeer`, for database edges and expired virtual-node edges |
| **Odigos** | When `k8s.namespace.name` is a dimension and the peer host is a Kubernetes Service (`mysql` in the client's namespace, or `mysql.<ns>.svc…`), names the server after the Service and sets `server_k8s.namespace.name`. |
| **Why** | These edges have no server span, so same-named Services in different namespaces merged into one node. |

---

## Service graph connector logging

- **Startup (once):** `servicegraphconnector started` with counts **and** lists: `extra_dimensions` / `virtual_node_peer_attributes` (same order as config / Helm), plus `virtual_node_feature_gate_enabled`.
