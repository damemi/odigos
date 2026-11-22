# Tech debt & known limitations

Running list of known shortcuts in the insights service. Each entry says what
the current behavior is, where it lives, why it was deferred, and the intended
fix. Add to this instead of letting TODOs scatter.

## Scoring engine

### Baseline JSONB is re-decoded on every merged observation
- **What**: `Merge` unmarshals the full baseline blob (e.g. all D1 edges) from
  JSONB and re-marshals it, an O(n) parse per observation per class. Scoring no
  longer pays this: `classState.decoded` holds the decoded baseline and the
  scorers take it via `baselineDecoder`, so a promoted (frozen) class is decoded
  once per residency instead of once per trace.
- **Where**: `Merge` in `internal/engine/scorer_d1.go` and the other scorers.
- **Why**: the merge path mutates what it decodes (`Histogram.Observe`) and
  rewrites the bytes anyway, so it cannot share the read path's cached value.
- **Fix**: keep the merged value decoded on `classState` too, re-encoding only
  when the flusher needs bytes. Lower value than the scoring fix was: merging
  stops once every class promotes. Note: changing the on-disk shape (array →
  JSON object) does NOT help - `json.Unmarshal` is O(n) either way and we'd
  lose stable ordering.

### D1 call edges still depend on upstream span-name normalization
- **What**: an edge is now `(from.service, from.op) → (to.service, to.op)`, so
  lateral movement between services is caught. But the `op` is still the raw
  span name, so high-cardinality names (URLs/IDs like `GET /items/42`) produce
  many near-duplicate edges if they aren't templatized first.
- **Where**: `edgesFromTrace` in `internal/engine/scorer_d1.go`.
- **Why**: relies on upstream URL-templatization (gateway action) to normalize
  span names before they reach the receiver.
- **Fix**: enforce / verify templatization, or normalize defensively in the
  receiver. Document the dependency on the gateway action.

### D6 table extraction is a heuristic, not a SQL parser
- **What**: when `db.collection.name` is absent we parse table names out of
  `db.query.text` by grabbing the token after `FROM`/`JOIN`/`INTO`/`UPDATE`.
  CTEs, subqueries, and comma-separated `FROM a, b` lists (beyond the first
  table) are not resolved. Statements with no parseable table fall back to a
  fixed-size hash of the normalized text (`#<sha1[:8]>`), so two genuinely
  different opaque statements that normalize identically collapse to one shape.
- **Where**: `parseSQLTables` / `buildQueryShape` in
  `internal/engine/scorer_d6.go`.
- **Why**: a real SQL grammar is heavy and dialect-specific; the heuristic
  covers the common single-table and JOIN cases cheaply on the hot path.
- **Fix**: if missed tables become a real gap, swap in a proper lightweight SQL
  parser (per dialect) behind the same `parseSQLTables` signature. The bounded
  hash fallback keeps storage safe regardless.

## Storage (Postgres)

### Additive counters can be over-counted if a flush is retried
- **What**: we save learning progress to Postgres in batches ("flushes"). We
  send *increments* ("+5 observations") and the upsert adds them. If a flush
  lands in the DB but the reply is lost, we retry and send the same "+5" again,
  so the database ends up with +10. Sets (D1/D2/D5/D6 members) are unaffected -
  re-adding the same thing is a no-op - only the additive numbers
  (`observation_count`, the D7 size histogram) can double.
- **Example**: a flush writes "+5 observations", the reply is lost, we retry.
  The database now shows 10 for a class that really saw 5.
- **Why it barely matters today**: the only real effect is a class *promoting*
  (going from learning to enforcing) a little early. But the seeded policies make
  that nearly a non-issue:
  - `D1/D2/D5/D6` are `match_mode=any` with a count of 50, so they already
    promote on count alone - the bug just makes them reach 50 a few real traces
    early. Negligible.
  - `D3/D4/D7` are `match_mode=all` with a 2–3 day minimum duration, so the
    *time* gate holds them back no matter how inflated the count is. The one
    additive class (D7) is here, so its double-count never promotes anything
    early - it only slightly skews the learned size distribution.
- **Where**: `ApplyBaselineDeltas` in `internal/db/postgres/baselines.go`;
  the flusher's retry in `internal/engine/baseline_store.go`.
- **The fix (deferred)**: give each flush an id and record applied ids in the
  same transaction as the deltas, skipping a flush whose id is already there.
  A retried flush is then a no-op. It needs a store refactor so a retry reuses
  the original flush id, so we do it only when
  promotion *timing* starts to matter (e.g. before GA, or if we ship count-only
  or low-threshold policies) - not in the current dev phase.

## Service / operations

### No runtime gating when the DB drops mid-flight
- **What**: startup is covered (the service waits/retries for the DB before
  serving, and liveness stays up while it does). But if the DB disappears
  *after* startup, the engine still attempts a write per incoming trace; each
  fails and is logged (now throttled). There's no circuit breaker that, after N
  consecutive failures, fast-drops traces and probes the DB every few seconds
  before resuming.
- **Where**: `engine.Process` → `repo.*` calls in `internal/engine/engine.go`;
  the startup path is in `cmd/main.go` (`connectRepoWithRetry`).
- **Why**: deliberately deferred - startup was the urgent fix; the runtime
  outage only wastes connection attempts + log lines, it doesn't crash.
- **Fix**: add a small circuit breaker around the repo calls: open after N
  consecutive connection failures, short-circuit (drop + count) while open,
  half-open probe on an interval, close on success. Keep it generic so it works
  for any backing store.

### One DB round-trip per trace (no batching)
- **What**: every trace does its own `UpsertTransaction`, baseline read, and
  baseline writes. At high trace volume that's a lot of small queries and a lot
  of connection pressure (very visible during a DB outage - one failed query
  per trace).
- **Where**: `engine.Process` in `internal/engine/engine.go`.
- **Why**: simplest correct version for the skeleton; correctness over
  throughput.
- **Fix**: batch writes - buffer transactions/observations and flush
  periodically or by size (single multi-row upsert), and/or cache the
  transaction-id lookup so repeat traffic skips the upsert. Pairs well with the
  circuit breaker above (a batch flush is also the natural place to trip it).

### HTTP and OTLP ports are hardcoded
- **What**: `:8080` (HTTP) and `:4317` (OTLP) are constants.
- **Where**: `cmd/main.go`.
- **Fix**: make configurable via env/flags when needed.

### The bundled Postgres is dev-grade; production should use an external DB
- **What**: `insights.database.provider=bundled` runs one Postgres pod with no
  replication, no backups, no TLS (DSN is `sslmode=disable`), and the default
  autovacuum. It is for development and light use. The production path today is
  `provider=external` (a managed Postgres: RDS / Cloud SQL / AlloyDB, or an
  operator-managed cluster) via `insights.database.external`.
- **Where**: `helm/odigos/templates/insights/database/`;
  `insights.database` in `values.yaml`.
- **Fix (planned)**: add `provider=cnpg` that renders a
  [CloudNativePG](https://cloudnative-pg.io/) `Cluster` CR for an in-cluster
  production Postgres - streaming replication + automated failover, WAL
  archiving + PITR to object storage, TLS, and PgBouncer pooling. The
  `provider` enum is the extension seam; adding `cnpg` is additive and touches
  no existing mode. Cheaper in-cluster hardening that does not need the operator:
  a NetworkPolicy limiting `:5432` to insights pods, `sslmode=require` with a
  mounted cert, a `securityContext.fsGroup` for restricted clusters, and a
  `postgres_exporter` sidecar for connection/bloat/long-transaction alerts
  (the Storage page already surfaces these on demand).
