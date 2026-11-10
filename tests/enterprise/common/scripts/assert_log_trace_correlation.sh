#!/bin/bash

# Asserts that a captured log line is correlated to the span that was active when it was written.
#
# The query files assert that a log record carries a trace id at all. This asserts the harder half:
# that the id it carries is the span we expect. It reads the log record's spanId back out and
# requires it to identify the server span that handled a specific request - same service, same
# route - inside the trace the log claims to belong to.
#
# Pinning the route matters. A service can have several server spans in one trace, so matching only
# on service and kind would accept a log correlated to the wrong request. Requiring the route makes
# the assertion name the exact span the log line was written from.
#
# This cannot be expressed as a single query because JMESPath has no join, and logs and spans are
# stored separately - deliberately so, since indexing logs by trace id would let log traffic add
# keys to the traces map.
#
# Usage: assert_log_trace_correlation.sh <service-name> <body-substring> <url-path>

set -e

SERVICE_NAME=$1
BODY_SUBSTRING=$2
URL_PATH=$3

if [ -z "$SERVICE_NAME" ] || [ -z "$BODY_SUBSTRING" ] || [ -z "$URL_PATH" ]; then
  echo "Usage: $0 <service-name> <body-substring> <url-path>"
  exit 1
fi

DEST_NAMESPACE="${4:-traces}"
DEST_SERVICE="${5:-simple-trace-db}"
DEST_PORT="4318"
PROXY="/api/v1/namespaces/$DEST_NAMESPACE/services/$DEST_SERVICE:$DEST_PORT/proxy"

function urlencode() (
  local length="${#1}"
  for ((i = 0; i < length; i++)); do
    local c="${1:i:1}"
    case $c in
    [a-zA-Z0-9.~_-]) printf "$c" ;;
    *) printf '%%%02X' "'$c" ;;
    esac
  done
)

echo "Asserting log/trace correlation for service '$SERVICE_NAME' on log body containing '$BODY_SUBSTRING'"

logs_query="[?(serviceName == '$SERVICE_NAME' && contains(bodyText, '$BODY_SUBSTRING') && traceId != null && spanId != null)]"
logs_response=$(kubectl get --raw "$PROXY/v1/logs?jmespath=$(urlencode "$logs_query")")

num_of_logs=$(echo "$logs_response" | jq 'length')
if [ "$num_of_logs" -lt 1 ]; then
  echo "Test FAILED: no correlated log record found for '$SERVICE_NAME'"
  exit 1
fi

trace_id=$(echo "$logs_response" | jq -r '.[0].traceId')
span_id=$(echo "$logs_response" | jq -r '.[0].spanId')
echo "Found correlated log record: traceId=$trace_id spanId=$span_id"

# the span the log points at must exist, belong to the same service, and be the server span that
# handled this specific route. anything else means the context attached to the log is not the
# context the request was running under.
traces_query="length([? span.spanId == '$span_id' && span.serviceName == '$SERVICE_NAME' && span.kind == 'server' && span.spanAttributes.\"url.path\" == '$URL_PATH']) > \`0\`"
traces_response=$(kubectl get --raw "$PROXY/v1/traces?jmespath=$(urlencode "$traces_query")")

if ! echo "$traces_response" | jq -e --arg trace_id "$trace_id" 'has($trace_id)' >/dev/null; then
  echo "Test FAILED: log claims trace $trace_id span $span_id, but that is not the '$SERVICE_NAME' server span for '$URL_PATH' in that trace"
  echo "Traces matching the span id: $(echo "$traces_response" | jq -c 'keys')"
  exit 1
fi

echo "Test PASSED: log record is correlated to the $SERVICE_NAME server span for $URL_PATH in trace $trace_id"
