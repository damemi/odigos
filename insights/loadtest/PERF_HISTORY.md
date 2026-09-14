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
  rather than learning - the state that matters in production

**Always normalise per trace.** Offered load drifts a few percent between runs;
raw `kubectl top` totals are not comparable on their own.

Two independent instruments are recorded because they cross-check each other:
cgroup CPU (`kubectl top`, whole cluster) and Go profiler samples (busiest pod).
On the 09-01 run they agreed to within half a point.

## Runs

| Run | Change | traces/s | Cluster CPU | **CPU/trace** | vs prev | vs baseline |
|-----|--------|----------|-------------|---------------|---------|-------------|
| `2026-08-30-hotpath` | baseline (none) | 1,963.8 | 2,301m | **1.172 ms** | - | - |
| `2026-08-31-lazy-otlp` | don't build a trace copy per service | 1,866.8 | 1,371m | **0.735 ms** | −37.3% | −37.3% |
| `2026-09-01-decode-cache` | decode each baseline once, not per trace | 1,869.7 | 1,018m | **0.545 ms** | −25.9% | **−53.5%** |
| `2026-09-14-shared-attrs-async-samples` (upstream leg) | re-baseline on `v1.37.0-rc1`, DB reset first | 1,890.2 | 954m | **0.505 ms** | −7.3% ¹ | −56.9% |
| `2026-09-14-shared-attrs-async-samples` (branch leg) | shared resource attrs on `Span` (no `mergeAttrs`) + async batched sample writer | 1,884.8 | 894m | **0.474 ms** | **−6.1%** | **−59.6%** |

¹ Not a code change: rc1 includes the lazy-OTLP encode (backlog item 1) and the
run started from a reset DB, so this row is the new reference point, and the
branch row is measured against it on the same day and cluster.

Busiest-pod profiler cross-check (same instrument both sides):

| Run | Pod | traces/s | samples / 30s | CPU/trace | vs prev |
|-----|-----|----------|---------------|-----------|---------|
| `2026-08-30-hotpath` | `pqzzz` | 393.0 | 7.66s (25.5%) | 0.650 ms | - |
| `2026-08-31-lazy-otlp` | `d46q2` | 466.5 | 5.33s (17.8%) | 0.381 ms | −41.4% |
| `2026-09-01-decode-cache` | `xvnr8` | 467.6 | 3.93s (13.1%) | 0.280 ms | −26.4% |
| `2026-09-14-…` (upstream leg) | `gmt5f` | 565.7 | 3.98s (13.3%) | 0.235 ms | −16.1% ¹ |
| `2026-09-14-…` (branch leg) | `nnxgw` | 658.0 | 4.47s (14.9%) | 0.226 ms | −3.5% |

The profiler sees a smaller delta than the cgroup (−3.5% vs −6.1%) because it
does not attribute GC background workers, which is where allocation savings land.

Supporting signals, latest run: heap ≈8.7 MB inuse (from 8.2 MB), RSS 20–21 Mi,
baseline cache 100% hit / 0 evictions / 47 resident per pod / 3.52 lookups per
trace, 138 flushes with 0 errors at 5.39 ms, `GetTransactionByID` 25.1/s at
2.36 ms. Cache and DB behaviour has been identical across all three runs, which
is what makes the CPU deltas trustworthy.

## Where the CPU goes now

Busiest pod `nnxgw` (branch leg of `2026-09-14`), 4.47s of samples over 30s at
658.0 traces/s.

| Cost | Samples | % CPU | Nature |
|------|---------|-------|--------|
| gRPC codec `proto.Unmarshal` (reflection) | 1.47s | 32.9% | request decode |
| `scoreEntry` | 0.54s | 12.1% | real comparison work |
| `Syscall6` (socket read/write) | 0.53s | 11.9% | network I/O |
| `DecodeRequest` | 0.31s | 6.9% | of which `decodeTraceTrees` 0.29s, `flattenAttrs` 0.21s |
| `guardrailRuntime.evaluate` | 0.06s | 1.3% | |

`extractTraceOTLP` and `mergeAttrs` no longer appear. From `alloc_space`
(11.6 GB cumulative):

| Allocator | Share | Note |
|-----------|-------|------|
| `reflect.unsafe_New` (protobuf) | 41.2% | reflection-based message alloc - now the clear lever |
| `decodeTraceTrees` total | 18.4% | was 31.2% on the upstream leg; `flattenAttrs` 13.4% is what remains |
| `scoreEntry` | 8.3% | per-trace fact extraction |
| `extractTraceSpans` | 1.9% | only for stored traces |

## Backlog, ranked

Estimates are of **total pod CPU** and are rough: direct cost is measured, the
GC follow-through is inferred by assuming GC scales with allocation share.

1. ~~**Defer the root trace's OTLP copy** - ~10–12%.~~ Shipped in `v1.37.0-rc1`;
   `extractTraceOTLP` is gone from the profile (`2026-09-14` upstream leg).
2. ~~**Stop copying resource attributes into every span** - ~7–9%.~~ Measured
   **−6.1%** on `2026-09-14` (branch `insights-hotpath-async-samples-shared-attrs`):
   `Span` keeps `SpanAttrs` and a shared `ResourceAttrs` map behind `Attr(key)`,
   `mergeAttrs` is removed, `decodeTraceTrees` allocation share 31% → 18%.
   The same branch also moves sample/evidence inserts to a background batched
   writer (1,050 synchronous inserts → 0 on the ack path during learning) but
   still does the first-use observation count synchronously (50–60 ms under
   load, once per transaction per replica) - finish that before claiming the
   learning-phase latency win.
3. **Generated protobuf unmarshal (vtprotobuf) for OTLP** - ~15–20%, the
   largest single item but also the largest change. Reflection-based decode is
   24.7% of CPU and 32% of allocations. Needs generated `UnmarshalVT` for the
   OTLP types plus a custom gRPC codec, and we don't own `go.opentelemetry.io/proto`,
   so it means vendoring generated variants. Do 1 and 2 first.
4. **Fixed-size span-ID keys instead of hex strings** - ~1%.
   `hex.EncodeToString` is 0.9% of allocations; a `[16]byte`/`[8]byte` map key
   would remove it. Cheap but marginal.
5. **Precompute per-class lookup sets in the decoded baseline** - ~1–2%.
   Tempting as a follow-on to the decode cache, but measured small: the
   allocations under `d1Data.score` are dominated by `edgesFromTrace` (per-trace
   facts, unavoidable) rather than building the `known` set. Listed so nobody
   re-derives it as a big win.

Not code changes, but worth knowing: `Syscall6` at 13.5% is socket I/O, and
larger OTLP batches from the sender would amortise it. Per-pod load skew in
these runs is a k6 connection artefact, not a balancing problem - the gateway
reconnects on its own in a real deployment, so do not "fix" it in the server.
