# Odigos Insights

Enterprise service that ingests distributed traces, learns per-transaction
baselines across **seven independent deviation classes** (D1..D7), scores
incoming traces against those baselines, and exposes results via an HTTP
API consumed by the UI.

This is the **skeleton** PR. Wiring is real; the schema is real; the OTLP
receiver decodes traces and hands them to a real (if minimal) scoring
engine. D1 (call edges), D2 (egress), D5 (libraries) actually learn and
score. See [Engine](#engine) below.

Known shortcuts and deferred work are tracked in [TECH_DEBT.md](TECH_DEBT.md).

## Layout

```
insights/
  cmd/main.go                              # entrypoint: config, logger, DB, schema init, gRPC + HTTP servers
  internal/
    repo/                                  # Repository interface + domain types (the DB-swap seam)
    db/clickhouse/                         # ClickHouse implementation (clickhouse-go/v2) + startup DDL
    api/http/                              # Gin HTTP server: /api/v1/* + /healthz + /readyz
    otlp/                                  # OTLP gRPC receiver - thin shell; hands traces to engine
    engine/                                # scoring core: decode, identify transaction, score, fire-or-merge
```

## Data model

The store is **ClickHouse**. It's designed around a **deviation class** model
where each class learns and scores independently and a configurable policy
decides when a combination of deviations fires an alert.

The engine keeps DB load off the per-trace hot path (in-memory baseline store,
identity/policy caches, and a write-behind flusher all sit above the
`repo.Repository` seam), and the baseline tables lean on ClickHouse's background
merges to do the cross-replica merge instead of locking and rewriting rows:

| table | engine | role |
|---|---|---|
| `transactions` | ReplacingMergeTree | identity of one (service, namespace, operation, kind) we baseline against. `transaction_id` is a deterministic hash of the identity (namespace included, so two same-named services in different namespaces stay distinct), so no auto-increment/upsert round-trip is needed. `kind` is `HTTP`, `GRPC`, `CONSUMER`, `PRODUCER`, `CRON`, `INTERNAL`, or anything new. |
| `transaction_activity` | AggregatingMergeTree | per-transaction volume (`sum`) + `last_seen` (`max`), written by the write-behind flusher in batches (never per trace). Keeps counting after promotion, so the UI can order transactions "most frequent first" and roll volume up per service. |
| `baseline_set_members` | ReplacingMergeTree | one row per member of a **set** baseline (D1 edges, D2 peers, D5 libs, D6 accesses/shapes). The union across flushes and replicas is free, and duplicate inserts collapse (idempotent, no dedup key). |
| `baseline_counters` | SummingMergeTree | one row per bucket of an **additive** baseline (the D7 payload-size histogram). Each flush appends a delta and ClickHouse sums. |
| `baseline_meta` | AggregatingMergeTree | per-class scalars: `observation_count` (summed), `promoted` and `saturated` (OR'd via max), `learning_started_at` (anchored via min), `data_schema_version` (max). |
| `trace_observations` | ReplacingMergeTree | sampled traces kept as UI examples per transaction. Stores the original OTLP protobuf bytes for just this trace's spans (`raw_otlp String`), so the UI gets Jaeger-grade fidelity (events, links, status, typed attributes) without an external trace backend. **NOT every trace is stored**; the engine keeps a bounded set (`example`) plus the trace that fired each anomaly (`anomaly_evidence`). A 120-day `TTL` matches the findings lookback ceiling. |
| `alert_policies` | ReplacingMergeTree(updated_at) | rules: which combinations of deviated classes fire. One policy per scope (`scope_type`/`scope_key`: global, service, or transaction); most-specific scope wins and fully replaces the broader one. Seeded with the global `default-any-1-of-8`. |
| `anomalies` | ReplacingMergeTree(version) | one row per fired alert, with per-class evidence so the UI explains itself. Acknowledgement re-inserts the row with a higher `version`. |
| `learning_policies` | ReplacingMergeTree(updated_at) | per-class learning-phase configuration: a `conditions` list combined by `match_mode` (`all`/`any`/`k_of` with `min_matches`), scoped by `scope_type`/`scope_key` (global, service, or transaction). Seeded at startup. See [Learning-phase configuration](#learning-phase-configuration). |

The engine owns the per-class JSON shapes; a `repo.BaselineCodec`
(`engine.NewBaselineCodec`) splits each class value into set members / additive
counters on write and rebuilds it on read, so the ClickHouse layer never has to
understand what a single class's bytes mean.

A transaction is whatever started the trace - HTTP request, gRPC method,
Kafka message, cron tick, background goroutine. Examples:

| service | operation | kind |
|---|---|---|
| user-service | `POST /checkout` | `HTTP` |
| user-service | `myapp.UserService/Login` | `GRPC` |
| billing-worker | `kafka.consume orders` | `CONSUMER` |
| billing-worker | `cron.nightly-rollup` | `CRON` |
| inventory-svc | `internal.reindex` | `INTERNAL` |

The built-in deviation classes (see `repo/repo.go` for the typed
constants and per-class examples):

- **D1_call_edges** - new (caller → callee) edges per transaction
- **D2_egress** - new downstream destinations per service
- **D3_latency** - per-span latency distribution / shape
- **D4_args_returns** - argument / return-value structural anomalies
- **D5_libraries** - new (code.namespace, library, version) tuples
- **D6_db_access** - new database access (system, namespace, table, operation)
  and new query shapes (operation + sorted tables)

Adding a class requires no schema change: add a constant and a scoring rule.
Cross-attribute relations are not a learned class; they are covered by the
attribute-correlation transaction guardrail.

## Changing the schema

Schema changes are versioned SQL migrations applied by
[golang-migrate](https://github.com/golang-migrate/migrate). The `.up.sql` /
`.down.sql` files live in `internal/db/clickhouse/migrate/migrations/` and are
embedded into the binary (`go:embed`), so the same image both serves traffic and
applies the schema. `migrate.Apply` runs them, tracking applied versions in a
`schema_migrations` table so each runs exactly once.

**Pre-release:** the schema is a single `000001_initial_schema` migration. While
Insights is still in development it is fine to edit that file and reset local
DBs (`insights/scripts/reset-db.sh`) rather than stacking incremental migrations.
Once we ship, treat numbered migrations as append-only again.

**Migrations run once per rollout, not per replica.** In Kubernetes the Helm
migration Job runs `insights migrate` (post-install / pre-upgrade); the service
replicas never migrate and stay not-ready (`SchemaReady`) until the Job has
finished. Locally, run it yourself before starting the service:

```sh
go run ./cmd migrate   # apply migrations
go run ./cmd           # start the service
```

To evolve the schema after release, **add a new numbered migration** (e.g.
`000002_add_team.up.sql` + `.down.sql`) - never edit an already-released one, or
clusters that ran the old version will diverge. Two ClickHouse gotchas:
- No transactional DDL: if a multi-statement file fails partway, golang-migrate
  marks the version *dirty* and refuses to proceed until it's forced. Keep
  statements retry-safe (`IF [NOT] EXISTS`) and simple.
- The multi-statement splitter splits on `;` literally - **never put a `;`
  inside a comment**, only inside real statements.

**Adding a column.** New migration with
`ALTER TABLE x ADD COLUMN IF NOT EXISTS ...` (cheap, metadata-only). Then add the
matching field to the domain type in `internal/repo/repo.go` and read/write it
in `internal/db/clickhouse/clickhouse.go`.

**Adding a new table.** New migration with its `CREATE TABLE IF NOT EXISTS`, add
methods to the `Repository` interface in `repo.go`, and implement them in
`clickhouse.go`.

**Changing a key/engine.** ClickHouse can't alter `ORDER BY` or the engine in
place; write a migration that creates a new table, copies data
(`INSERT INTO new SELECT FROM old`), then swaps via `RENAME`.

**Changing a deviation class's JSON shape.** Nothing schema-side: the value is
opaque to the store. Bump `DataSchemaVersion` in the engine when you write the
new shape, teach `engine.NewBaselineCodec` how to split/rebuild it if the layout
of members/counters changed, and have the engine handle old rows on read (either
re-learn or auto-upgrade).

**Adding a new deviation class.** No schema change needed. See the
comment on `DeviationClass` in `internal/repo/repo.go` for the recipe.

## Engine

The `internal/engine/` package is the scoring core. `Engine.Process(ctx, trace)`
runs for every trace the OTLP receiver decodes and does the following:

1. **Identify the transaction** from the root span: `(service, namespace, operation, kind)`.
   The id is a deterministic hash (no DB on the hot path); the `transactions`
   row is write-behind flushed with activity/baselines.
   `kind` is derived from `Span.Kind` + a few OTel attributes
   (`rpc.system=grpc` → `GRPC`, `faas.trigger=timer` → `CRON`, etc.).
   Traces whose root is an outbound CLIENT span are dropped silently
   (broken context propagation - see `discardableRoot` in
   `engine/transaction.go`).
2. **Load or seed the 6 baseline rows** for that transaction.
3. **Score** the trace against every class that is already `promoted`.
4. **Evaluate the alert policy** resolved for this transaction's scope
   (transaction override → service override → global default; default:
   `any-1-of-8`) against the deviated-class set.
5. **Fire-or-merge.** If a policy matches, insert an `anomalies` row with
   per-class evidence and a `trace_observations` row tagged
   `anomaly_evidence`, then return - the trace is NOT folded into the
   baseline (poisoning protection). Otherwise, merge the trace into every
   class, bump `observation_count`, and flip `promoted=true` for any class
   that meets its per-class learning policy (see
   [Learning-phase configuration](#learning-phase-configuration)). Time-based
   conditions are also re-evaluated after each write-behind flush, so a quiet
   transaction can graduate without waiting for the next trace.
6. Sample the trace as a `trace_observations` row for the UI.
   Transactions represent a *class* of traces (everything with the same
   root span); the engine keeps a small bounded set of representatives so
   the UI can render concrete examples. The
   sampling rules (see `sampler.go`):
   - `example` - at most one normal trace per transaction per
     `ExampleSampleInterval` (default 30s), capped at
     `ExamplesPerTransaction` (10) per transaction. Oldest evicted first
     so the set stays fresh. Throttling by time (rather than by trace
     count) keeps observation writes independent of the trace rate.
   - `anomaly_evidence` - the trace that fired an anomaly (1 per fire).
     Written by the anomaly path, not by the sampler.

### What's implemented

| Class | Scorer | Baseline shape | Deviates when |
|---|---|---|---|
| `D1_call_edges` | `scorer_d1.go` | `{"edges":[{"from":{svc,op},"to":{svc,op}}, ...]}` | a new `(service, op) → (service, op)` edge appears (a call into a new service deviates even if the op name is known elsewhere) |
| `D2_egress` | `scorer_d2.go` | `{"peers":[{host,port}, ...]}` | a CLIENT/PRODUCER span hits a new (host, port) |
| `D5_libraries` | `scorer_d5.go` | `{"libs":[{ns, ver}, ...]}` | a span's `ScopeName` is one we've never seen for this transaction |
| `D6_db_access` | `scorer_d6.go` | `{"accesses":[{system, ns, table, op}], "query_shapes":[{op, tables}]}` | a new (system, ns, table, op) access or a new query shape (op + sorted tables) appears |
| `D3_latency` | `scorer_d3.go` | latency histogram | entry-span latency exceeds the learned p99 |
| `D7_payload_size` | `scorer_d7.go` | request/response size histograms | a body size exceeds the learned p99 (a missing size counts as 0, so payload-less transactions still learn and stay silent) |

Histogram classes (`D3`, `D7`) store a fixed **32-bucket exponential**
histogram (bucket 0 = value 0; bucket k≥1 covers `[2^(k-1), 2^k)` of the
observed unit - µs for latency, bytes for payload). `GET …/baseline` includes a
chart-ready `histogram` object (`unit`, `layout_fingerprint`, `series[].bars[]`
with `lo`/`hi`/`count`/`label`) so the UI can plot without recomputing bounds
from opaque `data`.

#### How exponential buckets work (D3 / D7)

Observations are counted in fixed **exponential** bands (same family as
Prometheus native histograms / HdrHistogram - wide dynamic range, bounded memory):

| Band | Values counted |
|------|----------------|
| 0 | exactly 0 (no latency / empty body) |
| 1 | 1 unit only |
| k ≥ 1 | 2^(k−1) … 2^k − 1 (µs for latency, bytes for payload) |

**Example - latency with 32 buckets (µs):**

| Band | Range |
|------|-------|
| 10 | 512 µs – 1.0 ms |
| 15 | 16 ms – 32 ms |
| 17 | 65 ms – 131 ms |
| 20 | 0.5 s – 1.0 s |
| 31 (top) | ~18 min and above (everything larger shares this bar) |

### Adding a not-yet-implemented class

An unimplemented class has no scorer, so it never seeds a baseline and never
counts toward a transaction's learning/promotion state. To build one, implement
`engine.Scorer` (Class / Score / Merge), drop the file into `internal/engine/`,
and add it to `defaultScorers()`. No other file needs to change.

### Learning-phase configuration

Each class graduates from *learning* to *promoted* on its own schedule, driven
by a per-class **learning policy**: a list of **conditions** combined by a
**mode**.

| field | meaning |
|---|---|
| `conditions` | JSON list of criteria. Each is `{"type": ..., ...}`: `min_observations` (`{"type":"min_observations","minObservations":50}`), `min_duration` (`{"type":"min_duration","minDurationMinutes":1440}`, minutes because operators think in hours/days), `stable_observations` (`{"type":"stable_observations","stableObservations":300}`, promote after N observations with no new structure), and `stable_duration` (`{"type":"stable_duration","stableMinutes":10080}`, promote after the baseline has been structurally stable for N minutes). Unknown types are treated as unmet, so an older binary never promotes on a criterion it can't evaluate. |
| `match_mode` | how conditions combine: `all` = every condition (AND), `any` = one is enough (OR), `k_of` = at least `min_matches`. Empty/unknown defaults to `any`. |
| `min_matches` | the K for `match_mode=k_of`; ignored otherwise. |

A policy with **no conditions** falls back to a count-only default
(`DefaultMinObservations`, 50), so an unconfigured class still learns instead of
promoting on its first trace. `match_mode=any` is what lets a **rare**
transaction graduate: a once-a-week cron will never reach an observation count,
but the time window promotes it anyway. A busy endpoint under the same policy
promotes early via count. With `all`, a class needs both a large enough sample
and a long enough soak - used for the distribution/shape classes (D3/D4)
where stability matters more than speed.

Default policies (seeded by `000001_initial_schema`): every class promotes on
**stability** - "2 weeks structurally stable". Time only: a busy transaction
does not promote early on observation count, so weekly and bi-weekly patterns
are learned before the baseline freezes.

| class | conditions | match mode |
|---|---|---|
| `D1_call_edges` | 2 weeks stable (20160 minutes) | `all` |
| `D2_egress` | 2 weeks stable (20160 minutes) | `all` |
| `D3_latency` | 2 weeks stable (20160 minutes) | `all` |
| `D4_args_returns` | 2 weeks stable (20160 minutes) | `all` |
| `D5_libraries` | 2 weeks stable (20160 minutes) | `all` |
| `D6_db_access` | 2 weeks stable (20160 minutes) | `all` |
| `D7_payload_size` | 2 weeks stable (20160 minutes) | `all` |

**Storage.** The `learning_policies` table is the single source of truth.
Default rows are seeded at startup; the engine keeps the table in an in-memory
snapshot and consults that on the per-trace path (a plain map lookup), so it
**does not** query the DB per trace. A class with no row falls back to a
count-only default of `DefaultMinObservations` (50).

The snapshot is **refreshed at runtime**: a background reloader
(`runLearningPolicyReloader` in `cmd/main.go`, every 30s) re-reads the table and
swaps the snapshot atomically, so an edit takes effect within a tick **without a
restart** and without moving the read onto the hot path. A failed reload keeps
the previous snapshot.

### Saturated classes (per-set member cap)

A set class (D1 edges, D2 peers, D4 sites, D5 libraries, D6 tables) that grows
past `max_baseline_set_members` (system setting, default 1000) is not learning
a baseline, it is absorbing a per-request value that leaked into the
transaction's structure (an id in a span name, a per-request peer). Left alone
it would grow `baseline_set_members` without bound and, once the stability
window elapsed, promote a truncated set that flags every unseen member.

Instead the engine freezes the class as **saturated**:

- the in-memory value is cut to the cap and the class stops merging, counting
  and flushing members, so the store never receives the overflow;
- it never promotes and is never scored, even if force-promoted;
- it counts as **settled** for the transaction (`promoted` on the transaction,
  auto guardrail, recommendation mining), so one leaking class does not hold
  the rest of the transaction in learning forever;
- it is surfaced as `saturated` on the class, `saturated_classes` on the
  transaction, and the
  `insights_engine_baseline_classes_saturated_total` metric.

The flag lives in `baseline_meta.saturated` as a `max()` aggregate, like
`promoted`, so it is monotonic across replicas: whichever pod crosses the cap
first raises it, every other pod adopts it on its next flush or reload and
stops learning too. Rows that exceed the cap when loaded (written by an older
build, or a replica with a larger cap) are capped and flagged on load. Only a
class or transaction **reset** clears it - fix the identity leak first, or it
saturates again.

### Sending traces locally

The receiver speaks OTLP/gRPC on `:4317`. Easiest way to push a trace is
the upstream `telemetrygen` tool:

```sh
go install github.com/open-telemetry/opentelemetry-collector-contrib/cmd/telemetrygen@latest
telemetrygen traces --otlp-insecure --otlp-endpoint=localhost:4317 \
  --service my-svc --traces 1 --child-spans 2
```

Promotion follows the per-class
[learning policy](#learning-phase-configuration): with the seeded defaults,
D1/D2/D5 promote after ~50 normal traces while D3/D4 need ~100-200 (or
their time window). To graduate every class quickly in local dev, send ~200
traces or lower the counts in the `learning_policies` table and restart.
Promotion is visible in `GET /api/v1/transactions/{transaction_id}/baseline`. Once D1 is promoted, send
one trace with a brand-new child span name to fire a D1 deviation.

## Modularity

- All callers depend on `internal/repo.Repository`, never on a specific DB.
- Schema changes are versioned SQL migrations in
  `internal/db/clickhouse/migrations/`, applied by `clickhouse.Migrate`
  (golang-migrate) - run once per rollout, not per replica.

## Running locally

The service needs a reachable ClickHouse and an enterprise token. Spin up a
local server, then point the service at it:

```sh
docker run -d --name ch -p 9000:9000 -p 8123:8123 \
  -e CLICKHOUSE_DB=insights -e CLICKHOUSE_USER=insights \
  -e CLICKHOUSE_PASSWORD=insightspass \
  clickhouse/clickhouse-server:24.8-alpine

export CLICKHOUSE_DSN="clickhouse://insights:insightspass@localhost:9000/insights"

# Apply schema migrations once (the service no longer does this itself).
go run ./cmd migrate

ODIGOS_ONPREM_TOKEN="<your-token>" \
ODIGOS_LOG_LEVEL=debug \
go run ./cmd
```

The service listens on `:8080` (HTTP API) and `:4317` (OTLP gRPC). These are
hardcoded constants in `cmd/main.go` on purpose, configure it will be added in the future.

Env vars the service reads:

- `CLICKHOUSE_DSN` (required) - `clickhouse://user:pass@host:9000/db` URI.
- `ODIGOS_LOG_LEVEL` (`error|warn|info|debug`, default `info`).
- `ODIGOS_ONPREM_TOKEN` - required to pass
  `odigosauth.AssertEnterpriseOdigosToken` at startup.

## Building the image

From the `odigos-enterprise` repo root:

```sh
make build-insights TAG=v0.0.0-dev
```

Produces `registry.odigos.io/odigos-enterprise-insights:v0.0.0-dev`.
