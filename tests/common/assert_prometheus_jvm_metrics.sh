#!/usr/bin/env bash
# Assert that JVM runtime metrics exported by Odigos are present in Prometheus.
#
# Usage:
#   assert_prometheus_jvm_metrics.sh [service_name]
#
# Queries Prometheus via the Kubernetes API proxy and checks that each expected
# metric has at least one non-empty result for the given service.
set -euo pipefail

SERVICE_NAME="${1:-frontend}"
PROM_PROXY="/api/v1/namespaces/metrics/services/prometheus:9090/proxy/api/v1/query"

# Prometheus-normalized names for the OTel semconv JVM metrics emitted by
# odigosebpfreceiver (see ebpf-java-instrumentation/jvmmetrics/config.go).
#
# Gauges / counters / histograms follow OTel Prometheus translation:
#   jvm.memory.used (By)              -> jvm_memory_used_bytes
#   jvm.gc.duration (s, histogram)    -> jvm_gc_duration_seconds_count
#   jvm.cpu.time (s, monotonic sum)   -> jvm_cpu_time_seconds_total
#   jvm.class.loaded (gauge)          -> jvm_class_loaded  (no _total; receiver emits gauge)
#   jvm.cpu.recent_utilization (1)    -> jvm_cpu_recent_utilization_ratio
REQUIRED_METRICS=(
  'jvm_memory_used_bytes{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_memory_committed_bytes{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_memory_limit_bytes{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_class_loaded{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_class_count{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_thread_count{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_cpu_time_seconds_total{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_cpu_count{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_cpu_recent_utilization_ratio{service_name="'"${SERVICE_NAME}"'"}'
)

# These are skipped by the handler while their value is 0, so they only appear
# after a GC that happens while the probe is attached. Startup GCs typically
# occur before attach, so treat them as optional.
OPTIONAL_METRICS=(
  'jvm_class_unloaded{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_gc_duration_seconds_count{service_name="'"${SERVICE_NAME}"'"}'
  'jvm_memory_used_after_last_gc_bytes{service_name="'"${SERVICE_NAME}"'"}'
)

query_metric() {
  local query="$1"
  local encoded
  encoded=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1], safe=''))" "$query")
  kubectl get --raw "${PROM_PROXY}?query=${encoded}"
}

metric_exists() {
  local query="$1"
  local response
  response=$(query_metric "$query")
  local status result_len
  status=$(echo "$response" | jq -r '.status')
  result_len=$(echo "$response" | jq -r '.data.result | length')
  if [[ "$status" != "success" ]]; then
    echo "  query failed for: $query"
    echo "  response: $response"
    return 1
  fi
  if [[ "$result_len" -eq 0 ]]; then
    echo "  missing: $query"
    return 1
  fi
  echo "  found: $query (series=$result_len)"
  return 0
}

echo "Asserting JVM metrics in Prometheus for service_name=${SERVICE_NAME}"

failed=0
for query in "${REQUIRED_METRICS[@]}"; do
  if ! metric_exists "$query"; then
    failed=1
  fi
done

for query in "${OPTIONAL_METRICS[@]}"; do
  if ! metric_exists "$query"; then
    echo "  optional metric not present (ok): $query"
  fi
done

if [[ "$failed" -ne 0 ]]; then
  echo "ERROR: one or more required JVM metrics are missing"
  echo "--- sample of all jvm_* series ---"
  sample=$(query_metric '{__name__=~"jvm_.*"}' || true)
  echo "$sample" | jq -r '.data.result[].metric.__name__' 2>/dev/null | sort -u || echo "$sample"
  exit 1
fi

echo "All required JVM metrics are present"
exit 0
