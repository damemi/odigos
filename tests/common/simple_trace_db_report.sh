#!/bin/bash

# Prints a summary of the traces stored in simple-trace-db, to debug trace queries that do not match.
#
# Usage: simple_trace_db_report.sh [namespace] [service]
# Defaults to the simple-trace-db service in the traces namespace.

set -e

dest_namespace="${1:-traces}"
dest_service="${2:-simple-trace-db}"

traces_file=$(mktemp)
trap 'rm -f "$traces_file"' EXIT
kubectl get --raw "/api/v1/namespaces/$dest_namespace/services/$dest_service:4318/proxy/v1/traces" > "$traces_file"

echo "==== Traces in simple-trace-db: $(jq 'length' "$traces_file")"

echo "==== Span count per service"
jq -r '[.[][] | .serviceName] | group_by(.) | .[] | "\(.[0]): \(length)"' "$traces_file"

echo "==== Services per trace"
jq -r 'to_entries[] | "\(.key): \([.value[].serviceName] | unique | join(","))"' "$traces_file"

# simple-trace-db drops a trace from every query when one of its spans has a parent missing from the trace
# https://github.com/odigos-io/simple-trace-db/blob/v0.0.12/src/spans-structure/reverse-trace-tree.ts
echo "==== Spans whose parent is missing from the trace"
jq -r 'to_entries[] | .key as $trace | [.value[].spanId] as $ids | .value[] | select((.parentSpanId // "") != "" and (.parentSpanId | IN($ids[]) | not)) | "\($trace): \(.serviceName) \(.kind) \(.name) span=\(.spanId) parent=\(.parentSpanId)"' "$traces_file"

echo "==== Spans of traces with more than one service"
jq -r 'to_entries[] | select([.value[].serviceName] | unique | length > 1) | "-- trace \(.key)", (.value | sort_by(.startTime)[] | "\(.startTime) \(.serviceName) \(.kind) \(.name) span=\(.spanId) parent=\(.parentSpanId // "")")' "$traces_file"
