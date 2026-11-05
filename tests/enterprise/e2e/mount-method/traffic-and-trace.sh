#!/usr/bin/env bash
# Generate traffic and retry until a full distributed trace is collected.
# Cold-restarted workloads (notably the Ruby geolocation service) need a few
# requests before context propagation kicks in, so several bursts may be needed.
set -uo pipefail

# Runs from the scenario directory; the odigos checkout is a sibling of odigos-enterprise.
common="../../../../odigos/tests/common"
job_url="${common}/apply/generate-traffic-job.yaml"
query_runner="${common}/simple_trace_db_query_runner.sh"

while true; do
  kubectl delete job buybot-job -n default --ignore-not-found >/dev/null 2>&1
  kubectl apply -f "$job_url" >/dev/null
  kubectl wait --for=condition=complete job/buybot-job -n default --timeout=60s >/dev/null 2>&1 || true
  if bash "$query_runner" wait-for-trace.yaml; then
    break
  fi
  sleep 3
done

kubectl delete job buybot-job -n default --ignore-not-found >/dev/null 2>&1
