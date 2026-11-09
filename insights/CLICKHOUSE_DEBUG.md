# ClickHouse debug cheatsheet

Quick reference for poking at the insights service's ClickHouse from a cluster.

## Connect

```bash
PW=$(kubectl get secret odigos-insights-clickhouse -n odigos-system -o jsonpath='{.data.password}' | base64 -d)
kubectl exec -it -n odigos-system deploy/odigos-insights-clickhouse -- \
  clickhouse-client -u odigos_insights --password "$PW" -d odigos_insights
```

You're now in a SQL prompt against the `odigos_insights` database.

## Handy queries

> Most tables are `ReplacingMergeTree` / `AggregatingMergeTree`, so add `FINAL`
> (or `GROUP BY`) to collapse background-merge duplicates when you want exact rows.

**List tables**

```sql
SHOW TABLES;
```

**Transactions, most frequent first** (identity + learned volume)

```sql
SELECT t.namespace, t.service, t.operation, t.kind,
       sum(a.volume) AS volume, max(a.last_seen) AS last_seen
FROM transactions FINAL AS t
LEFT JOIN transaction_activity AS a ON a.transaction_id = t.transaction_id
GROUP BY t.namespace, t.service, t.operation, t.kind
ORDER BY volume DESC
LIMIT 20;
```

**Baseline for one transaction** (per deviation class)

```sql
SELECT class, observation_count, promoted, learning_started_at
FROM baseline_meta FINAL
WHERE transaction_id = <ID>
ORDER BY class;
```

**Learned set members** (D1 edges / D2 peers / D5 libs / D6 accesses)

```sql
SELECT class, member
FROM baseline_set_members FINAL
WHERE transaction_id = <ID>
ORDER BY class, member;
```

**Recent anomalies**

```sql
SELECT id, transaction_id, policy_id, triggered_classes, scored_at
FROM anomalies FINAL
ORDER BY scored_at DESC
LIMIT 20;
```
