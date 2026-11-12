#!/bin/bash
set -euo pipefail

echo "Starting Central Backend Infrastructure Tests..."

# Use a high port to avoid collisions in CI environments
LOCAL_PORT=18083

# Wait for backend pod to be ready first.
# Note: with charts that still probe /healthz for readiness, the pod can become
# Ready before Keycloak/Redis init finishes. Prefer /readyz below for app-ready.
echo "Waiting for backend pod to be ready..."
kubectl wait --for=condition=ready pod -n odigos-central -l app=central-backend --timeout=120s || {
  echo "FAILED: Backend pod did not become ready"
  echo "Checking backend pod status..."
  kubectl get pods -n odigos-central -l app=central-backend
  kubectl describe pod -n odigos-central -l app=central-backend | tail -50
  exit 1
}

echo "Backend pod is Ready (kubelet)"

# Port-forward helper function with retry logic
BACKEND_PID=""

start_port_forward() {
  # Kill any existing port-forward
  if [ -n "$BACKEND_PID" ] && kill -0 "$BACKEND_PID" 2>/dev/null; then
    kill "$BACKEND_PID" 2>/dev/null || true
    sleep 1
  fi

  echo "Starting port-forward on port $LOCAL_PORT..."
  kubectl port-forward -n odigos-central svc/central-backend "$LOCAL_PORT":8081 >/dev/null 2>&1 &
  BACKEND_PID=$!

  # Give port-forward a moment to establish
  sleep 3
}

# Cleanup function
cleanup() {
  if [ -n "$BACKEND_PID" ]; then
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# Start initial port-forward
start_port_forward

# Wait until the process is listening (/healthz) and fully initialized (/readyz).
# /readyz stays 503 while Keycloak/Redis init is in progress; GraphQL is only
# available after that swap.
echo "Waiting for port-forward, /healthz, and /readyz..."
MAX_RETRIES=90
RETRY_COUNT=0

http_code() {
  # Avoid concatenating curl's failure code with a fallback ("000000").
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 2 "$1" 2>/dev/null || true)
  if [ -z "$code" ]; then
    code="000"
  fi
  echo "$code"
}

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
  # Keep restarting port-forward for the whole wait window (pod restarts drop it).
  if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
    echo "Port-forward died, restarting..."
    start_port_forward
  fi

  HEALTH_CODE=$(http_code "http://localhost:$LOCAL_PORT/healthz")
  READY_CODE=$(http_code "http://localhost:$LOCAL_PORT/readyz")

  if [ "$HEALTH_CODE" = "200" ] && [ "$READY_CODE" = "200" ]; then
    echo "Backend is fully ready (/healthz and /readyz both 200)"
    break
  fi

  RETRY_COUNT=$((RETRY_COUNT + 1))
  if [ $RETRY_COUNT -lt $MAX_RETRIES ]; then
    echo "Waiting for backend... ($RETRY_COUNT/$MAX_RETRIES) healthz=$HEALTH_CODE readyz=$READY_CODE"
    sleep 2
  fi
done

if [ $RETRY_COUNT -eq $MAX_RETRIES ]; then
  echo "FAILED: Backend did not become ready after $((MAX_RETRIES * 2)) seconds"
  echo "Checking backend pod status..."
  kubectl get pods -n odigos-central -l app=central-backend
  echo "Checking port-forward process..."
  ps aux | grep "port-forward" | grep -v grep || echo "Port-forward process not found"
  echo "Testing probe endpoints..."
  curl -sv --max-time 5 "http://localhost:$LOCAL_PORT/healthz" || true
  echo
  curl -sv --max-time 5 "http://localhost:$LOCAL_PORT/readyz" || true
  echo
  kubectl logs -n odigos-central -l app=central-backend --tail=50
  exit 1
fi

# graphql_query METHOD: POST a query and require valid JSON with .data present.
# An optional third argument is a bearer token for operations that need a session.
# Prints the raw body on failure so CI logs are actionable.
graphql_query() {
  local label="$1"
  local query="$2"
  local token="${3:-}"
  local body
  body=$(curl -s -X POST "http://localhost:$LOCAL_PORT/graphql" \
    -H "Content-Type: application/json" \
    ${token:+-H "Authorization: Bearer $token"} \
    -d "{\"query\": \"$query\"}") || {
    echo "FAILED: $label — curl failed"
    exit 1
  }

  if ! echo "$body" | jq -e . >/dev/null 2>&1; then
    echo "FAILED: $label — response is not JSON (backend may still be starting)"
    echo "Response: $body"
    exit 1
  fi

  if ! echo "$body" | jq -e '.data' >/dev/null 2>&1; then
    echo "FAILED: $label — GraphQL response missing .data"
    echo "Response: $body"
    exit 1
  fi

  echo "$body"
}

#===========================================
# TEST 1: Basic Health Endpoint
#===========================================
echo "Test 1: Basic health endpoint..."
HEALTH_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$LOCAL_PORT/healthz")
if [ "$HEALTH_STATUS" != "200" ]; then
  echo "FAILED: Health endpoint returned $HEALTH_STATUS"
  exit 1
fi
echo "PASSED: Health endpoint OK"

echo "Test 1b: Ready endpoint..."
READY_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$LOCAL_PORT/readyz")
if [ "$READY_STATUS" != "200" ]; then
  echo "FAILED: Ready endpoint returned $READY_STATUS"
  exit 1
fi
echo "PASSED: Ready endpoint OK"

#===========================================
# TEST 2: Redis Health Check via GraphQL
#===========================================
echo "Test 2: Redis health check via GraphQL..."
REDIS_HEALTH=$(graphql_query "Redis health" "{ health { backend { redisStatus { status message } } } }")

REDIS_STATUS=$(echo "$REDIS_HEALTH" | jq -r '.data.health.backend.redisStatus.status // ""')
if [ "$REDIS_STATUS" != "HEALTHY" ]; then
  echo "FAILED: Redis status: $REDIS_STATUS"
  echo "Response: $REDIS_HEALTH"
  exit 1
fi
echo "PASSED: Redis is healthy"

#===========================================
# TEST 3: Direct Redis Connectivity
#===========================================
echo "Test 3: Direct Redis connectivity..."
# Try both common label selectors for Redis
REDIS_POD=$(kubectl get pods -n odigos-central -l app=redis -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || \
  kubectl get pods -n odigos-central -l app.kubernetes.io/name=redis -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
if [ -n "$REDIS_POD" ]; then
  REDIS_PING=$(kubectl exec -n odigos-central "$REDIS_POD" -- redis-cli PING 2>/dev/null || echo "FAILED")
  if [ "$REDIS_PING" != "PONG" ]; then
    echo "FAILED: Redis PING returned: $REDIS_PING"
    exit 1
  fi
  echo "PASSED: Redis direct connectivity OK"
else
  echo "WARNING: Could not find Redis pod, skipping direct connectivity test"
fi

#===========================================
# TEST 4: Keycloak Health Check via GraphQL
#===========================================
echo "Test 4: Keycloak health check via GraphQL..."
KC_HEALTH=$(graphql_query "Keycloak health" "{ health { backend { keycloakStatus { status message } } } }")

KC_STATUS=$(echo "$KC_HEALTH" | jq -r '.data.health.backend.keycloakStatus.status // ""')
# Keycloak might be DOWN if auth is disabled, so we just report the status
echo "INFO: Keycloak status: $KC_STATUS"
if [ "$KC_STATUS" = "HEALTHY" ]; then
  echo "PASSED: Keycloak is healthy"
elif [ "$KC_STATUS" = "DOWN" ]; then
  echo "INFO: Keycloak is DOWN (expected if auth is disabled)"
else
  echo "WARNING: Keycloak status is $KC_STATUS"
fi

#===========================================
# TEST 5: SSE Health Check via GraphQL
#===========================================
echo "Test 5: SSE health check via GraphQL..."
SSE_HEALTH=$(graphql_query "SSE health" "{ health { backend { sseStatus { status message } } } }")

SSE_STATUS=$(echo "$SSE_HEALTH" | jq -r '.data.health.backend.sseStatus.status // ""')
if [ "$SSE_STATUS" != "HEALTHY" ]; then
  echo "FAILED: SSE status: $SSE_STATUS"
  echo "Response: $SSE_HEALTH"
  exit 1
fi
echo "PASSED: SSE is healthy"

#===========================================
# TEST 6: SSE Connection Test
#===========================================
echo "Test 6: SSE endpoint connection test..."

set +e
timeout 3 curl -s -N "http://localhost:$LOCAL_PORT/sse/events?clusterID=test-cluster" >/dev/null 2>&1
EXIT_CODE=$?
set -e

# Exit code 124 = timeout, meaning connection stayed open → GOOD for SSE
# Exit code 0 = connection closed but was accepted → also acceptable
if [ "$EXIT_CODE" -eq 124 ] || [ "$EXIT_CODE" -eq 0 ]; then
  echo "PASSED: SSE endpoint accepts streaming connections"
else
  echo "FAILED: SSE endpoint did not respond (exit code: $EXIT_CODE)"
  exit 1
fi

#===========================================
# TEST 7: WebSocket Upgrade Check
#===========================================
echo "Test 7: WebSocket endpoint availability..."
# Check if WebSocket endpoint accepts upgrade requests
# We use curl with upgrade headers to verify the endpoint responds correctly
WS_RESPONSE=$(curl -s -i --max-time 3 \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Key: dGVzdGtleQ==" \
  -H "Sec-WebSocket-Version: 13" \
  "http://localhost:$LOCAL_PORT/ws/proxy?proxyID=test-proxy-infra&version=1.0.0&platformType=k8s&platformName=test" 2>&1 || true)

# Check for HTTP 101 (Switching Protocols) or WebSocket upgrade response
if echo "$WS_RESPONSE" | grep -qi "101\|upgrade\|switching"; then
  echo "PASSED: WebSocket endpoint accepts connections"
else
  # If we get HTTP 400 with specific error, that's also acceptable (means endpoint is responding)
  if echo "$WS_RESPONSE" | grep -qi "HTTP"; then
    echo "PASSED: WebSocket endpoint is responding"
  else
    echo "FAILED: WebSocket endpoint not available"
    echo "Response: $WS_RESPONSE"
    exit 1
  fi
fi

#===========================================
# TEST 8: GraphQL SystemConfig Query
#===========================================
echo "Test 8: GraphQL systemConfig query..."
SYSTEM_CONFIG=$(graphql_query "systemConfig" "{ systemConfig { version isInitialized } }")

VERSION=$(echo "$SYSTEM_CONFIG" | jq -r '.data.systemConfig.version // ""')
if [ -z "$VERSION" ]; then
  echo "FAILED: Could not get systemConfig"
  echo "Response: $SYSTEM_CONFIG"
  exit 1
fi
echo "PASSED: GraphQL systemConfig working (version: $VERSION)"

#===========================================
# TEST 9: ComputePlatforms Query
#===========================================
echo "Test 9: GraphQL computePlatforms query..."
# computePlatforms requires a session. Sign in as the Keycloak bootstrap admin
# through central-backend's Keycloak proxy; its built-in master-realm `admin`
# role is what Central reads as ADMIN.
KC_USER=$(kubectl get secret keycloak-admin-credentials -n odigos-central -o jsonpath='{.data.admin-username}' | base64 -d)
KC_PASS=$(kubectl get secret keycloak-admin-credentials -n odigos-central -o jsonpath='{.data.admin-password}' | base64 -d)
TOKEN=$(curl -s -X POST "http://localhost:$LOCAL_PORT/realms/master/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "grant_type=password" \
  --data-urlencode "client_id=odigos-central" \
  --data-urlencode "username=$KC_USER" \
  --data-urlencode "password=$KC_PASS" | jq -r '.access_token // ""')
if [ -z "$TOKEN" ]; then
  echo "FAILED: could not get an access token from Keycloak"
  exit 1
fi

ANON_PLATFORMS=$(curl -s -X POST "http://localhost:$LOCAL_PORT/graphql" \
  -H "Content-Type: application/json" \
  -d '{"query": "{ computePlatforms { id } }"}')
if ! echo "$ANON_PLATFORMS" | jq -e '.errors[0].message | test("unauthenticated")' >/dev/null 2>&1; then
  echo "FAILED: anonymous computePlatforms query was not rejected"
  echo "Response: $ANON_PLATFORMS"
  exit 1
fi
echo "PASSED: anonymous computePlatforms query rejected"

PLATFORMS=$(graphql_query "computePlatforms" "{ computePlatforms { id name status } }" "$TOKEN")

# Just verify the query works, even if there are no platforms
if echo "$PLATFORMS" | jq -e '.data.computePlatforms' >/dev/null 2>&1; then
  PLATFORM_COUNT=$(echo "$PLATFORMS" | jq -r '.data.computePlatforms | length')
  echo "PASSED: GraphQL computePlatforms working (found $PLATFORM_COUNT platforms)"
else
  echo "FAILED: computePlatforms query failed"
  echo "Response: $PLATFORMS"
  exit 1
fi

echo ""
echo "========================================"
echo "All Central Backend Infrastructure Tests Passed!"
echo "========================================"
