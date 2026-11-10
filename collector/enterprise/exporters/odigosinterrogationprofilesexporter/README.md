# Odigos Interrogation Profiles Exporter

Profiles-only exporter for the interrogation loop. Writes linked sample stacks
to the shared cacheDb Redis (logical DB 1, separate from URL-templatization on
DB 0) as an immutable Hash per trace. The insights service later correlates
those stacks into call-trie structure in ClickHouse.

- Key: `odigos:interrogation:profile:<hex(trace)>`
- Field: `<hex(span)>:<hex(sha1(value)[:8])>`, so each distinct stack (sample
  type + frames) of a span is kept once. All samples in a profile share its
  time, so time is not part of the field.
- Value: JSON stack blob (`st` + root-first `frames`). JDK/VM and agent frames are
  dropped; library/framework frames are kept, each consecutive run collapsed to its
  root-most frame. Depth is capped at 64, dropping library frames before app frames.
- Cap: 500 fields per trace hash; further stacks are dropped
- Whole-trace TTL is `2 × group_by_trace_wait_duration` (gateway groupbytrace
  wait), refreshed when a new field is added

Stacks are appended to a flat pending list on a single batcher goroutine
(non-blocking; full channel drops). A flush runs when `flush_threshold` stacks
are pending (default 500) or `flush_interval` elapses (default 5s), then issues
one Lua EVAL per trace key for the batch.

| Status | |
| --- | --- |
| Stability | development |
| Distributions | `odigos-enterprise-collector` only |

## Configuration

```yaml
exporters:
  odigos_interrogation_profiles:
    redis_endpoint: odigos-cache.odigos-system:6379
    group_by_trace_wait_duration: 30s  # Redis key TTL = 2× this
    # flush_interval: 5s
    # flush_threshold: 500

service:
  pipelines:
    profiles:
      exporters: [odigos_interrogation_profiles, otlp_grpc/profiles-to-ui]
```
