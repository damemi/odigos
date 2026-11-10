# Odigos Group By Trace Processor

The **odigosgroupbytrace** processor buffers spans until their trace is complete and then releases the whole trace as one batch, so processors after it (tail sampling, Insights) see full traces. Odigos installs it on the gateway when trace aggregation is needed, in place of the contrib `groupbytrace` processor.

Unlike contrib `groupbytrace`, it releases a trace as soon as it looks complete instead of always waiting the full window, holds traces with async (Kafka/queue) spans longer, moves spans instead of copying them, and rejects new data under memory pressure instead of growing until OOM.

## When a trace is released

| Trace | Released |
| ----- | -------- |
| Has a PRODUCER/CONSUMER span (async) | `async_wait` after its first span |
| Root span seen, no async spans | `root_settle` after the last span arrived, never later than `sync_wait` |
| No root span yet | `sync_wait` after its first span |
| Buffer full (`max_traces`) | Oldest trace released immediately |

## Configuration

```yaml
processors:
  odigosgroupbytrace:
    sync_wait: 30s               # longest an HTTP/RPC trace (no async spans) is held
    async_wait: 30s              # longest a trace with PRODUCER/CONSUMER spans is held
    root_settle: 7s              # release once the root arrived and no span came for this long; 0 disables
    max_traces: 200000           # buffer cap; past it the oldest trace is released early
    late_spans_max_memory_mib: 70 # memory for remembering released trace IDs (late_spans); 0 disables
```

All fields are optional; the values above are the defaults.

## Internal metrics

In Prometheus the names get the `otelcol_` prefix and `.` becomes `_`.

| Metric | Attributes | Meaning |
| ------ | ---------- | ------- |
| `odigos.groupbytrace.traces_in_memory` | | Traces currently buffered. |
| `odigos.groupbytrace.spans_in_memory` | | Spans currently buffered. |
| `odigos.groupbytrace.traces_released` | `class`, `reason` | Traces sent on, by why they were released. |
| `odigos.groupbytrace.spans_released` | `class`, `reason` | Spans sent on, by why their trace was released. |
| `odigos.groupbytrace.late_spans` | `class`, `reason` | Spans that arrived after their trace was already released, so the trace went out incomplete. |
| `odigos.groupbytrace.late_traces` | `class`, `reason` | Traces that reached the next consumer (tail sampling, Insights, the backend) in more than one piece. Counted once per trace. |
| `odigos.memory_pressure.spans` | `component`, `action` | Spans rejected (retryable) because the Go memory limit was reached. |

- `class`: `sync` or `async`.
- `reason`: `settled`, `timeout`, `evicted`, `shutdown`; on `late_spans` also `long_running`.

## Late spans

The processor guesses when a trace is complete, and `late_spans` measures how often that guess was wrong: spans that arrived after their trace was released. They are not dropped; they are buffered again and released as a second piece of the trace, so the backend gets the trace split in two and tail sampling may decide on each piece separately.

Released trace IDs are remembered for one minute (bounded by `late_spans_max_memory_mib`), and a late span is labeled with how its trace was released, which points to the setting to change:

| `reason` | What happened | What to change |
| -------- | ------------- | -------------- |
| `settled` | The root span arrived, `root_settle` passed with no new spans, then more spans came | Raise `root_settle` |
| `timeout` | The trace was held for the full `sync_wait` / `async_wait` | Raise that wait |
| `evicted` | The buffer was full and the trace was released early | Raise `max_traces` |
| `long_running` | The span itself lasted longer than the wait for its trace (gRPC streams, k8s watches, websockets, long polls). A span is sent only when it ends, so it always arrives after its trace was released | Nothing; no reasonable wait catches it |

A late span shorter than the wait keeps the release reason: it was delayed on the way (queues, retries), and a longer wait could have caught it.

`late_spans` measures how much data arrived late; `late_traces` measures how many traces were split. A trace is counted in `late_traces` once, on its first late batch, with `long_running` if that batch has a span longer than the wait and the release reason otherwise.
