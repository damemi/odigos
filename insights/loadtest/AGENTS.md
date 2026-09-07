# AGENTS.md - Insights loadtest

Guidance for agents working in `insights/loadtest/` or analyzing a load run against Insights.

## What this loadtest does

- Deploys a k6 + `xk6-client-tracing` Deployment that **pushes multi-service OTLP traces directly** to `odigos-insights-headless:4317` (bypasses the Odigos gateway).
- Tunables via env / `make`: `VUS`, `SLEEP_MS`, `DURATION`, `ENDPOINT`, `TRACE_NAMESPACE`, `TENANTS`.
- `script.js` defines 6 weighted scenarios over ~18 services, covering HTTP, gRPC, Kafka produce/consume, 4 datastore engines and one Odigos-probe span - **20 transactions per tenant**. `TENANTS=N` replays all of it under N `service.namespace` values, so it is the knob for resident-transaction pressure.

## Cardinality is a contract, not an accident

Every value Insights turns into a baseline key is a literal in `script.js`, and the Dockerfile patches the generator's random scope and peer ports. That is deliberate: a synthetic run must **converge** on a fixed finding count, otherwise the findings list drowns in fake anomalies and hides whatever the run was meant to measure.

Before changing `script.js`, know which fields are keys:

| Dimension | Keyed on | Safe to randomize? |
|-----------|----------|--------------------|
| D1 call edges | `(service.name, span name)` of each parent/child pair | no |
| D2 egress | `server.address` + port on CLIENT/PRODUCER spans | no |
| D3 latency | entry span duration (histogram) | **yes** |
| D4 args/returns | `code.function.name` + `arg.N` format classes | no |
| D5 libraries | instrumentation scope name/version | no (pinned in Dockerfile) |
| D7 db access | `db.system`/`namespace`/`collection`/`operation` + query shape | no |
| D8 payload size | `http.{request,response}` body size (histogram) | **yes** |

Two traps that cost us a run:

- `tracing.SEMANTICS_DB` **emits nothing** - `TemplatedGenerator` only implements the HTTP one. DB spans must set `db.*` by hand or D7 learns nothing at all.
- An unset `http.response.status_code` is filled randomly from `{200,201,202,204}`, and it is a transaction identity dimension - so every endpoint silently becomes 4 transactions, each learning from a quarter of the traffic. Pin it on every entry span.

To verify a `script.js` change, run k6 against a local collector with a file exporter and confirm the distinct D1/D2/D7 sets are the same at 100 traces and at 10k.
- Image: `public.ecr.aws/e1u7m2k3/insights-k6-runner` (build with `./push.sh`; linux/amd64 by default).

## Expected send rate (sanity-check before quoting throughput)

k6 logs report **iterations** = one `client.push(gen.traces())` each.

Approximate upper bound (ignoring Export RTT):

```
iters/s ≈ VUS / (SLEEP_MS/1000 + avg_push_seconds)
```

With defaults `VUS=5`, `SLEEP_MS=50` and a fast Export, expect **~80–100 iter/s** from one runner replica.

Always compare:

1. k6 `complete` iterations delta / Δt
2. Sum of `insights_engine_traces_processed_total` across **all** insights pods / Δt

If processed ≪ sent, investigate drops (`discardableRoot` / decode failures), Export errors in k6 logs, or Insights saturation - do **not** quietly report the lower number as “load generated”.

Gateway is **out of path** for this loadtest; do not blame gateway unless `ENDPOINT` was pointed at it.

## Capturing metrics + pprof

Prefer an in-cluster curl pod against insights pod IPs (port-forward from the agent host is flaky).

Collect at least:

- `/metrics` snapshots (both pods) spaced ≥60s apart under steady load
- CPU: `/debug/pprof/profile?seconds=30` (port 6060)
- Heap: `/debug/pprof/heap` (inuse_space)

From each metrics scrape, always record **cache size gauges** (see template). Metric names:

| Metric | Meaning |
|--------|---------|
| `insights_baseline_cache_resident_transactions` | Transactions held in the in-memory baseline store |
| `insights_baseline_cache_dirty_transactions` | Resident txs with unflushed deltas |
| `insights_baseline_cache_evictions_total` | LRU evictions (counter; report Δ over the window) |
| `insights_baseline_cache_lookups_total` | Baseline cache hit/miss style lookups (by label if present) |
| `insights_txn_cache_lookups_total` | Transaction-identity cache lookups |
| `insights_policy_cache_lookups_total` | Alert/learning policy cache lookups |

Save under `loadtest/results/<timestamp>/` (gitignored). **Do not** write a separate opaque `SUMMARY.md` - put everything in one `ANALYSIS.md`.

Because `results/` is gitignored, also add the run's comparable numbers to
[`PERF_HISTORY.md`](./PERF_HISTORY.md) - the tracked cross-run ledger of CPU per
trace and the ranked optimisation backlog. Detail stays in `ANALYSIS.md`; only
what survives a clean checkout goes in the ledger.

## ANALYSIS.md template (required shape)

When asked to analyze a load run, write **only** `results/<run-id>/ANALYSIS.md` using this structure:

```markdown
# Loadtest analysis - <run-id>

## Setup
- Cluster / context:
- Insights replicas / image:
- k6 image / VUS / SLEEP_MS / replicas / ENDPOINT:
- Notes (e.g. scope-cardinality patch, HPA present?):

## Send vs process rate
- k6 iterations/s (from logs, measured window):
- insights_engine_traces_processed_total /s (sum of pods):
- Ratio processed/sent:
- Conclusion (match / drops / backpressure):

## Cache sizes
Report **gauges at end of window** (and start→end if they moved). One row per insights pod.

| Cache signal | Pod A | Pod B | Notes |
|--------------|-------|-------|-------|
| baseline resident (`insights_baseline_cache_resident_transactions`) | | | in-memory txn count |
| baseline dirty (`insights_baseline_cache_dirty_transactions`) | | | awaiting write-behind flush |
| baseline dirty/resident ratio | | | high + rising ⇒ flush lag |
| baseline evictions Δ (`insights_baseline_cache_evictions_total`) | | | >0 under load ⇒ LRU pressure |
| baseline lookups Δ (`insights_baseline_cache_lookups_total`) | | | break out hit/miss labels if present |
| txn cache lookups Δ (`insights_txn_cache_lookups_total`) | | | |
| policy cache lookups Δ (`insights_policy_cache_lookups_total`) | | | |

Interpretation bullets (required):
- Is resident stable or climbing with load?
- Is dirty backlog growing (flush not keeping up)?
- Any evictions? Correlate with heap pprof / flush_duration.

## Metrics (key signals)
| Signal | Pod A | Pod B | Notes |
|--------|-------|-------|-------|
| traces_processed Δ | | | |
| subtransactions_processed Δ | | | |
| flushes ok / errors | | | |
| flush_duration avg | | | |
| repo Ping errors | | | |

## CPU pprof
- Bottleneck (top cum frames):
- Link to files:
  - `pprof/<pod>-cpu-30s.pb.gz`
- How to open:
  `go tool pprof -http=:8081 pprof/<pod>-cpu-30s.pb.gz`

## Heap pprof
- Total inuse + top allocators:
- Link to files:
  - `pprof/<pod>-heap.pb.gz`
- How to open:
  `go tool pprof -http=:8091 -sample_index=inuse_space pprof/<pod>-heap.pb.gz`
- Call out whether heap aligns with baseline cache resident growth.

## Scaling / HPA
- metrics-server available? `kubectl top pods -n odigos-system`
- HPA status / replica changes during run:
- If no metrics-server: say so; do not invent scale events.

## Verdict
- 3–5 bullets: what hurts, what is fine, what to change for the next run (VUS/SLEEP_MS/replicas).
```

Keep language concrete; cite numbers and **exact file paths** an engineer can open locally.

## Commands agents should know

```bash
make -C insights loadtest-start          # or: VUS=20 SLEEP_MS=10 make -C insights loadtest-start
make -C insights loadtest-stop
make -C insights loadtest-restart
make -C insights/loadtest push           # via ./push.sh - rebuild/push runner image
```
