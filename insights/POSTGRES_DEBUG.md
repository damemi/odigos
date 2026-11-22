# Postgres debug cheatsheet

Quick reference for poking at the insights service's bundled Postgres from a cluster.

## Connect

```bash
kubectl exec -it -n odigos-system deploy/odigos-insights-db -- \
  psql -U odigos_insights -d odigos_insights
```

psql connects over the pod's local socket, so no password is needed. You're now
in a SQL prompt against the `odigos_insights` database. The Insights UI Storage
page shows the same health data (connections, top statements, table sizes,
retention) without a shell.

## Handy queries

**List tables with sizes**
```sql
SELECT relname, n_live_tup, pg_size_pretty(pg_total_relation_size(relid))
FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC;
```

**Transactions, most frequent first** (identity + learned volume)
```sql
SELECT t.namespace, t.service, t.operation, t.kind, a.volume, a.last_seen
FROM transactions t
LEFT JOIN transaction_activity a USING (transaction_id)
ORDER BY a.volume DESC NULLS LAST
LIMIT 20;
```

**Baseline for one transaction** (per deviation class)
```sql
SELECT class, observation_count, promoted, saturated, learning_started_at
FROM baseline_meta WHERE transaction_id = <ID> ORDER BY class;
```

**Learned set members** (D1 edges / D2 peers / D5 libs / D6 accesses)
```sql
SELECT class, member FROM baseline_set_members
WHERE transaction_id = <ID> ORDER BY class, member;
```

**Recent anomaly issues**
```sql
SELECT transaction_id, signature, triggered_classes, occurrences, max_score, last_seen
FROM anomaly_issues ORDER BY last_seen DESC LIMIT 20;
```

**Slowest statements** (pg_stat_statements)
```sql
SELECT calls, round(total_exec_time) AS total_ms, round(mean_exec_time::numeric, 2) AS mean_ms, left(query, 120)
FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 10;
```

**Retention job's last run**
```sql
SELECT * FROM maintenance_runs;
```
