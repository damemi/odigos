#!/bin/bash

# Runs simple-trace-db query files and compares the number of results to the expectation in each file.
# Prints a PASS/FAIL line per query and fails when any query fails.
#
# Usage: simple_trace_db_query_runner.sh [--wait] [-n namespace] [-s service] <query-file>...
#   --wait  retry until all queries pass, until the chainsaw step times out.
#           On timeout prints the raw responses of the failing queries.
#   -n, -s  simple-trace-db namespace and service, default traces/simple-trace-db.
#
# A query file may set `endpoint` to query a signal other than traces, e.g. `endpoint: /v1/logs`.
# /v1/traces returns an object keyed by trace id, so the count is traces. /v1/logs returns an array,
# so the count is log records.

set -uo pipefail

wait=false
namespace=traces
service=simple-trace-db
query_files=()
while [ $# -gt 0 ]; do
  case "$1" in
  --wait) wait=true ;;
  -n) namespace="$2" && shift ;;
  -s) service="$2" && shift ;;
  -*) query_files=() && break ;;
  *) query_files+=("$1") ;;
  esac
  shift
done
if [ ${#query_files[@]} -eq 0 ]; then
  echo "Usage: $0 [--wait] [-n namespace] [-s service] <query-file>..."
  exit 1
fi

print_responses=false

# Prints "PASS <file>: ..." or "FAIL <file>: ...", fails on FAIL.
run_query() {
  local file="$1" query endpoint expected minimum response count
  if ! query=$(yq e '.query' "$file" 2>&1); then
    echo "FAIL $file: failed to read: $query"
    return 1
  fi
  endpoint=$(yq e '.endpoint // "/v1/traces"' "$file")
  expected=$(yq e '.expected.count' "$file")
  minimum=$(yq e '.expected.minimum' "$file")
  if [ "$query" = "null" ] || { [ "$expected" = "null" ] && [ "$minimum" = "null" ]; }; then
    echo "FAIL $file: invalid query file, it needs a query and expected.count or expected.minimum"
    return 1
  fi

  if ! response=$(kubectl get --request-timeout=10s --raw "/api/v1/namespaces/$namespace/services/$service:4318/proxy$endpoint?jmespath=$(jq -rn --arg q "$query" '$q | @uri')" 2>&1); then
    echo "FAIL $file: request failed: $response"
    return 1
  fi
  if ! count=$(jq 'length' <<<"$response" 2>/dev/null); then
    echo "FAIL $file: unexpected response: $response"
    return 1
  fi

  if [ "$expected" != "null" ]; then
    [ "$count" -eq "$expected" ] && echo "PASS $file: expected $expected got $count" && return 0
    echo "FAIL $file: expected $expected got $count"
  else
    [ "$count" -ge "$minimum" ] && echo "PASS $file: expected at least $minimum got $count" && return 0
    echo "FAIL $file: expected at least $minimum got $count"
  fi
  [ "$print_responses" = true ] && jq . <<<"$response"
  return 1
}

run_queries() {
  local file failed=0
  for file in "${query_files[@]}"; do
    run_query "$file" || failed=1
  done
  return $failed
}

if [ "$wait" = false ]; then
  run_queries
  exit
fi

source "$(dirname "$0")/wait_until.sh"

last_results=""
check_queries() {
  local results
  results=$(run_queries) && echo "$results" && return 0
  # Print only changes, to keep the log readable.
  if [ "$results" != "$last_results" ]; then
    echo "[$(date +%T)] waiting for queries:"
    echo "$results"
    last_results="$results"
  fi
  return 1
}

print_failed_responses() {
  echo "raw responses of the failing queries:"
  print_responses=true
  run_queries
}

wait_until check_queries print_failed_responses
