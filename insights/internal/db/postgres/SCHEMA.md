# Insights DB schema (Postgres)

How the tables relate, grouped by concern. Replicas write deltas, and each
table's upsert decides how a delta merges with the stored row, so concurrent
replicas never overwrite each other.

## Identity & traffic

| Table | Key | Merge on write |
|---|---|---|
| `transactions` | `transaction_id` | Overwrite. The id is a hash of `(service, namespace, operation, kind, source identity)`. |
| `transaction_activity` | `transaction_id` | `volume` summed, `last_seen` max. Powers "most active" ordering. |

## Baselines - the learned "normal", per (transaction, class)

| Table | Key | Merge on write |
|---|---|---|
| `baseline_set_members` | `(transaction_id, class, md5(member))` | Insert-if-missing. One row per learned member of a set class (D1 edges, D2 egress, D5 libraries, D6 db access). |
| `baseline_counters` | `(transaction_id, class, bucket)` | `count` summed. Additive classes (D7 payload-size histogram). |
| `baseline_meta` | `(transaction_id, class)` | `observation_count` summed, `promoted`/`saturated` OR'd, `learning_started_at` min, the rest max. A class exists iff it has a meta row. |

A flush writes all three tables in one transaction.

## Policies - configuration, scoped global → service → transaction

| Table | Key | Merge on write |
|---|---|---|
| `learning_policies` | `(class, scope_type, scope_key)` | Overwrite. When a class graduates from learning to scored. |
| `alert_policies` | `(scope_type, scope_key)` | Overwrite. Risk-score threshold, signal weights, enricher lists, delivery. |
| `service_guardrails` | `(scope_type, scope_key)` | Overwrite. User-declared allowlists per rule, each in enforce / suggest / off. |
| `system_settings` | `id` (single row) | Overwrite. Engine-wide operational settings. |

## Findings - what an operator acts on

| Table | Key | Merge on write |
|---|---|---|
| `anomaly_issues` | `(transaction_id, signature)` | `occurrences` summed, `max_score`/`last_seen`/decision timestamps max, `first_seen` min, descriptive columns follow the newest occurrence. |
| `guardrail_violations` | `(scope_type, scope_key, rule_key, offending)` | Same as `anomaly_issues`. |
| `guardrail_recommendations` | `(scope_type, scope_key, recommendation_id)` | Overwrite per mining run; state set by the operator. |
| `recommendation_live_counts` | `(scope_type, scope_key, recommendation_id, rule_key)` | `held`/`broken` summed, `first_at` min, `last_at` max. |
| `recommendation_mining_runs` | `transaction_id` | Overwrite. Newest run and the replica's soft claim. |

## Evidence

| Table | Key | Merge on write |
|---|---|---|
| `trace_observations` | `(transaction_id, sample_reason, trace_id)` | Overwrite. Raw OTLP of sampled traces (examples, finding evidence, relation samples). |

## Topology - service graph (blast radius)

| Table | Key | Merge on write |
|---|---|---|
| `service_graph_edges` | `(server, client, connection_type, collector_pod)` | Overwrite only with a newer `observed_at`. Reads sum counts across pods. |

## Notifications

| Table | Key | Merge on write |
|---|---|---|
| `integrations` | `integration_id` | Overwrite. |
| `notification_dispatch` | `(integration_id, finding_key)` | Overwrite. Delivery ledger and retry schedule. |
| `notification_cursors` | `integration_id` | Overwrite. Notifier progress, leader only. |
| `notification_attempts` | none (append-only) | Audit trail. |

## Retention

The leader runs an hourly retention job instead of per-table TTLs. It deletes
`relation` samples after 14 days, other samples after
`system_settings.observation_retention_days`, and delivery attempts after 30
days, then records the outcome in `maintenance_runs` for the Storage page.

Unset timestamps are stored as the Unix epoch rather than NULL, so the
GREATEST/LEAST merges and the Go side treat "never" uniformly.
