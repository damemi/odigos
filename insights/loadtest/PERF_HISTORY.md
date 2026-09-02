# Insights performance history

Running ledger of Insights hot-path performance across load runs, so each change
can be compared to the one before it *and* to the original baseline.

This file is tracked in git on purpose: `loadtest/results/` is gitignored, so
the per-run `ANALYSIS.md` files live only on the machine that produced them.
Keep the detail there; keep the comparable numbers here.

**When you finish a load run:** add a row to the table below, refresh "Where the
CPU goes now", and re-rank the backlog. Cite the run-id so the full analysis is
findable on whoever's machine still has it.

## Comparability rules

Numbers are only comparable if the run used the same knobs. All runs below used:

- 8 insights replicas, limits 1 CPU / 1Gi, requests 100m / 128Mi
- k6 `insights-k6-runner:v0.0.3`, `VUS=20 SLEEP_MS=10 TENANTS=2`, 1 replica,
  straight to `odigos-insights-headless:4317` (gateway out of path)
- HPA present (min 2 / max 8), pinned at 8 throughout
- Cluster `arn:aws:eks:us-east-1:233171357117:cluster/tamir-insights`
- ClickHouse already warm, so baselines are promoted and the engine is scoring
  rather than learning — the state that matters in production

**Always normalise per trace.** Offered load drifts a few percent between runs;
raw `kubectl top` totals are not comparable on their own.

Two independent instruments are recorded because they cross-check each other:
cgroup CPU (`kubectl top`, whole cluster) and Go profiler samples (busiest pod).
On the 09-01 run they agreed to within half a point.

## Runs

| Run | Change | traces/s | Cluster CPU | **CPU/trace** | vs prev | vs baseline |
|-----|--------|----------|-------------|---------------|---------|-------------|
| `2026-08-30-hotpath` | baseline (none) | 1,963.8 | 2,301m | **1.172 ms** | — | — |
| `2026-08-31-lazy-otlp` | don't build a trace copy per service | 1,866.8 | 1,371m | **0.735 ms** | −37.3% | −37.3% |
| `2026-09-01-decode-cache` | decode each baseline once, not per trace | 1,869.7 | 1,018m | **0.545 ms** | −25.9% | **−53.5%** |

Busiest-pod profiler cross-check (same instrument both sides):

| Run | Pod | traces/s | samples / 30s | CPU/trace | vs prev |
|-----|-----|----------|---------------|-----------|---------|
| `2026-08-30-hotpath` | `pqzzz` | 393.0 | 7.66s (25.5%) | 0.650 ms | — |
| `2026-08-31-lazy-otlp` | `d46q2` | 466.5 | 5.33s (17.8%) | 0.381 ms | −41.4% |
| `2026-09-01-decode-cache` | `xvnr8` | 467.6 | 3.93s (13.1%) | 0.280 ms | −26.4% |

Supporting signals, latest run: heap ≈8.7 MB inuse (from 8.2 MB), RSS 20–21 Mi,
baseline cache 100% hit / 0 evictions / 47 resident per pod / 3.52 lookups per
trace, 138 flushes with 0 errors at 5.39 ms, `GetTransactionByID` 25.1/s at
2.36 ms. Cache and DB behaviour has been identical across all three runs, which
is what makes the CPU deltas trustworthy.

## Where the CPU goes now

Busiest pod `xvnr8`, 3.93s of samples over 30s at 467.6 traces/s.

| Cost | Samples | % CPU | Nature |
|------|---------|-------|--------|
| gRPC codec `proto.Unmarshal` (reflection) | 0.97s | 24.7% | request decode |
| GC (`mallocgc` + `gcBgMarkWorker`) | 0.94s | 23.9% | follows allocation |
| `Syscall6` (socket read/write) | 0.53s | 13.5% | network I/O |
| `extractTraceOTLP` | 0.43s | 10.9% | **addressable** |
| `scoreEntry` | 0.39s | 9.9% | real comparison work now |
| `decodeTraceTrees` | 0.20s | 5.1% | of which `mergeAttrs` 0.12s |
| `guardrailRuntime.evaluate` | 0.08s | 2.0% | |

Allocation is now the lever on GC. From `alloc_space` (11.08 GB cumulative):

| Allocator | Share | Note |
|-----------|-------|------|
| `reflect.unsafe_New` (protobuf) | 32.0% | reflection-based message alloc |
| `decodeTraceTrees` total | 29.3% | `mergeAttrs` 14.5% + `flattenAttrs` 10.9% |
| `extractTraceOTLP` | 9.3% | incl. `proto.Marshal` 8.0% |
| `scoreEntry` | 7.2% | mostly per-trace fact extraction, not baseline |

## Backlog, ranked

Estimates are of **total pod CPU** and are rough: direct cost is measured, the
GC follow-through is inferred by assuming GC scales with allocation share.

1. ~~**Defer the root trace's OTLP copy** — ~10–12%.~~ Implemented on branch
   `insights-encode-otlp-on-demand`, awaiting a run. `DecodeRequest` encoded
   every root trace even though only the few stored ones need the bytes; it now
   holds each trace's spans decoded and encodes in `RawOTLP()`. Also removes the
   encode/decode round trip `subTraces` used to do to re-root a nested entry.
2. **Stop copying resource attributes into every span** — ~7–9%.
   `decodeTraceTrees` calls `mergeAttrs(resourceAttrs, spanAttrs)` per span,
   allocating a fresh map holding a full copy of the resource attributes for
   each of ~8.5 spans per trace. Keep resource attrs shared and look through to
   them instead. Costs 3.8% CPU directly and a quarter of all allocations.
   Effort is the catch: ~36 non-test `.Attrs` reads become an accessor.
3. **Generated protobuf unmarshal (vtprotobuf) for OTLP** — ~15–20%, the
   largest single item but also the largest change. Reflection-based decode is
   24.7% of CPU and 32% of allocations. Needs generated `UnmarshalVT` for the
   OTLP types plus a custom gRPC codec, and we don't own `go.opentelemetry.io/proto`,
   so it means vendoring generated variants. Do 1 and 2 first.
4. **Fixed-size span-ID keys instead of hex strings** — ~1%.
   `hex.EncodeToString` is 0.9% of allocations; a `[16]byte`/`[8]byte` map key
   would remove it. Cheap but marginal.
5. **Precompute per-class lookup sets in the decoded baseline** — ~1–2%.
   Tempting as a follow-on to the decode cache, but measured small: the
   allocations under `d1Data.score` are dominated by `edgesFromTrace` (per-trace
   facts, unavoidable) rather than building the `known` set. Listed so nobody
   re-derives it as a big win.

Not code changes, but worth knowing: `Syscall6` at 13.5% is socket I/O, and
larger OTLP batches from the sender would amortise it. Per-pod load skew in
these runs is a k6 connection artefact, not a balancing problem — the gateway
reconnects on its own in a real deployment, so do not "fix" it in the server.
