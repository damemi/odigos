# odigos_url_templatization exporter

Enterprise-only side-channel on the gateway traces pipeline. It counts HTTP paths that are still
untemplated after `odigosurltemplate` (when that processor is present) and writes those counts to
the `odigos-cache` Redis. Downstream live-traffic learning uses the counts to propose URL
templatization rules.

The exporter does **not** mutate spans. Destinations keep receiving the same traces; this component
only observes a copy.

## Motivation

HTTP span names should be `{method} {target}` with a **low-cardinality** target (`/users/{id}`, not
`/users/1234`). Frameworks sometimes set `http.route` on server spans; `url.template` on client
spans is almost never set.

The OSS `odigosurltemplate` processor fills that gap with custom rules and heuristics. Rules still
have to come from somewhere: operators can write them, or Odigos can learn them from traffic.

Learning needs a stable, bounded sample of paths that the processor did **not** already treat as a
custom rule. Putting that work in an exporter keeps it off the destination path, so Redis latency
or a full hash cannot stall export to backends.

## When it is added

Odigos appends this exporter to the traces pipeline only when:

1. Live-traffic learning is enabled (`cardinalityControl.urlTemplatization.liveTrafficLearning`).
2. The cluster is enterprise (community never installs the side-channel).
3. Traces are already enabled (a traces destination, or Insights).

When `odigosurltemplate` is also configured (from a URL-templatization Action), it runs
**before** this exporter, so counts reflect post-processor spans.

## What is counted

Only HTTP **server** and **client** spans with a request method (`http.request.method` or
`http.method`) and a resolvable path (`url.path` / `http.target` / `url.full` / `http.url`).

A span is **skipped** when:

- Kind is not server or client.
- The processor/framework already set `http.route` (server) or `url.template` (client) **and**
  either there is no `odigos.url_templatization.result`, or the result is `custom_rule`.

Heuristic, path-normalization, and static-path results **are** counted so learning can still see
those paths.

Workload identity comes from the `odigos_config_k8s` extension (namespace / kind / name /
container). Spans whose resource cannot be resolved are dropped from the count.

## How counts are stored

Each increment is a Redis hash field:

| Span kind | Key |
| --- | --- |
| server | `odigos:urltempl:unmatched:server:{workload}` |
| client | `odigos:urltempl:unmatched:client:{workload}` |

The field name is the URL path; the value is an integer count.

Path deltas are sent to a single batcher loop over a non-blocking channel (full
channel drops the path so the traces pipeline never waits on Redis). The loop
merges counts in memory and writes when:

- the number of distinct `(redis key, path)` pairs reaches `flush_threshold` (default 500), or
- `flush_interval` elapses (default 5s).

Shutdown drops any in-memory pending batch; these are samples, not a complete count.

Only that loop talks to Redis, so writes never overlap. If Redis is slow, the
channel fills and further unmatched paths are dropped until the loop is receiving
again.

A Lua script applies the batch in one round-trip:

- Existing fields always increment; `HEXPIRE` refreshes a sliding idle TTL (`path_idle_ttl`).
- New fields are added only while `HLEN < max_paths_per_workload`. Server and client hashes are
  capped independently. When the hash is full, new paths are skipped; existing paths still increment.

`max_paths_per_workload` and `path_idle_ttl` are required. The autoscaler always sets them from
live-traffic learning config when it wires the exporter.

## Configuration

Autoscaler-generated example:

```yaml
exporters:
  odigos_url_templatization:
    odigos_config_extension: odigos_config_k8s
    endpoint: odigos-cache.odigos-system:6379
    max_paths_per_workload: 5000
    path_idle_ttl: 48h

service:
  pipelines:
    traces/in:
      exporters: [odigosrouterconnector/traces, odigos_url_templatization]
```

`odigos_config_extension`, `endpoint`, `max_paths_per_workload`, and `path_idle_ttl` are required.
`flush_interval` and `flush_threshold` keep their factory defaults unless set explicitly.
