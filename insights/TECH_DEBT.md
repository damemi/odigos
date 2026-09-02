# Tech debt & known limitations

Running list of known shortcuts in the insights service. Each entry says what
the current behavior is, where it lives, why it was deferred, and the intended
fix. Add to this instead of letting TODOs scatter.

## Scoring engine

### D4 / D6 are not implemented
- **What**: D4 (args/returns) and D6 (attr relations) have no scorer, so they
  never learn, deviate, or seed a baseline, and never count toward a
  transaction's learning/promotion state. They remain in the risk catalog as
  known-but-unbuilt dimensions.
- **Where**: `defaultScorers()` in `internal/engine/scorer.go` (they are simply
  absent); catalog/weights in `internal/risk/risk.go`.
- **Fix**: implement real `Scorer`s and add them to `defaultScorers()`.

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
  JSON object) does NOT help — `json.Unmarshal` is O(n) either way and we'd
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

### D7 table extraction is a heuristic, not a SQL parser
- **What**: when `db.collection.name` is absent we parse table names out of
  `db.query.text` by grabbing the token after `FROM`/`JOIN`/`INTO`/`UPDATE`.
  CTEs, subqueries, and comma-separated `FROM a, b` lists (beyond the first
  table) are not resolved. Statements with no parseable table fall back to a
  fixed-size hash of the normalized text (`#<sha1[:8]>`), so two genuinely
  different opaque statements that normalize identically collapse to one shape.
- **Where**: `parseSQLTables` / `buildQueryShape` in
  `internal/engine/scorer_d7.go`.
- **Why**: a real SQL grammar is heavy and dialect-specific; the heuristic
  covers the common single-table and JOIN cases cheaply on the hot path.
- **Fix**: if missed tables become a real gap, swap in a proper lightweight SQL
  parser (per dialect) behind the same `parseSQLTables` signature. The bounded
  hash fallback keeps storage safe regardless.

## Storage (ClickHouse)

### Additive counters can be over-counted if a flush is retried
- **What**: we save learning progress to ClickHouse in batches ("flushes"). We
  send *increments* ("+5 observations") and ClickHouse sums them. If a flush
  lands in the DB but the reply is lost, we retry and send the same "+5" again,
  so ClickHouse ends up with +10. Sets (D1/D2/D5/D7 members) are unaffected —
  re-adding the same thing is a no-op — only the additive numbers
  (`observation_count`, the D8 size histogram) can double.
- **Example**: a flush writes "+5 observations", the reply is lost, we retry.
  ClickHouse now shows 10 for a class that really saw 5.
- **Why it barely matters today**: the only real effect is a class *promoting*
  (going from learning to enforcing) a little early. But the seeded policies make
  that nearly a non-issue:
  - `D1/D2/D5/D7` are `match_mode=any` with a count of 50, so they already
    promote on count alone — the bug just makes them reach 50 a few real traces
    early. Negligible.
  - `D3/D4/D6/D8` are `match_mode=all` with a 2–3 day minimum duration, so the
    *time* gate holds them back no matter how inflated the count is. The one
    additive class (D8) is here, so its double-count never promotes anything
    early — it only slightly skews the learned size distribution.
- **Where**: `ApplyBaselineDeltas` in `internal/db/clickhouse/clickhouse.go`;
  the flusher's retry in `internal/engine/baseline_store.go`.
- **The fix (deferred)**: stop sending "add 5" and instead have each replica
  write "my running total is 5" under its own id, into a `ReplacingMergeTree`
  that keeps only the latest row per id. Re-sending the same total then does
  nothing (like a set), and the real total is the sum across ids at read time —
  which also stays correct across replicas and restarts. It's a bigger change
  (recreates the two additive tables + a store refactor), so we do it only when
  promotion *timing* starts to matter (e.g. before GA, or if we ship count-only
  or low-threshold policies) — not in the current dev phase.

### A failed migration leaves the DB "dirty" and needs manual recovery
- **What**: migrations run via golang-migrate. ClickHouse has no transactional
  DDL, so if a migration fails partway golang-migrate marks that version *dirty*
  and every later run (including the migration Job's retries) refuses to proceed
  until an operator forces the version. There is no automatic rollback.
- **Where**: `migrate.Apply` in `internal/db/clickhouse/migrate/`; the migration
  files in `internal/db/clickhouse/migrate/migrations/`; the Helm migration Job.
- **Why**: inherent to golang-migrate + ClickHouse. Mitigated by keeping
  migrations retry-safe (`IF [NOT] EXISTS`) and simple.
- **Fix**: prefer one statement per migration file so a partial multi-statement
  apply can't happen; document/automate the `migrate force <version>` recovery
  runbook. Revisit a declarative tool (e.g. Atlas) if/when the schema grows
  materialized views/projections — note Atlas's ClickHouse driver is paid, so
  it can't ship in the OSS chart as-is.

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
- **Why**: deliberately deferred — startup was the urgent fix; the runtime
  outage only wastes connection attempts + log lines, it doesn't crash.
- **Fix**: add a small circuit breaker around the repo calls: open after N
  consecutive connection failures, short-circuit (drop + count) while open,
  half-open probe on an interval, close on success. Keep it generic so it works
  for the next backing store, not just ClickHouse.

### One DB round-trip per trace (no batching)
- **What**: every trace does its own `UpsertTransaction`, baseline read, and
  baseline writes. At high trace volume that's a lot of small queries and a lot
  of connection pressure (very visible during a DB outage — one failed query
  per trace).
- **Where**: `engine.Process` in `internal/engine/engine.go`.
- **Why**: simplest correct version for the skeleton; correctness over
  throughput.
- **Fix**: batch writes — buffer transactions/observations and flush
  periodically or by size (single multi-row upsert), and/or cache the
  transaction-id lookup so repeat traffic skips the upsert. Pairs well with the
  circuit breaker above (a batch flush is also the natural place to trip it).

### HTTP and OTLP ports are hardcoded
- **What**: `:8080` (HTTP) and `:4317` (OTLP) are constants.
- **Where**: `cmd/main.go`.
- **Fix**: make configurable via env/flags when needed.
