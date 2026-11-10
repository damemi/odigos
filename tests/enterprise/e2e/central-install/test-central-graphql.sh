#!/bin/bash
set -euo pipefail

echo "🚀 Testing Central GraphQL Integration..."

# Port forward to services
kubectl port-forward -n odigos-central svc/keycloak 8082:8080 >/dev/null 2>&1 &
KEYCLOAK_PID=$!
kubectl port-forward -n odigos-central svc/central-backend 8083:8081 >/dev/null 2>&1 &
BACKEND_PID=$!

# Cleanup function
cleanup() {
  kill $KEYCLOAK_PID $BACKEND_PID 2>/dev/null || true
}
trap cleanup EXIT

# Wait for port forwards
echo "⏳ Waiting for port forwards to be ready..."
sleep 5

# central-backend serves /healthz before Keycloak init and /readyz only after
# full startup. With charts that still use /healthz for readiness, the Deployment
# can be Ready while GraphQL is still unavailable — wait explicitly for /readyz.
echo "⏳ Waiting for central-backend /readyz (up to 3 minutes)..."
READY_OK=0
for i in $(seq 1 90); do
  CODE=$(curl -s -o /dev/null -w "%{http_code}" --max-time 2 "http://localhost:8083/readyz" || echo "000")
  if [ "$CODE" = "200" ]; then
    echo "✅ central-backend /readyz is 200"
    READY_OK=1
    break
  fi
  echo "⏳ /readyz=$CODE ($i/90)..."
  sleep 2
done
if [ "$READY_OK" != "1" ]; then
  echo "❌ central-backend /readyz did not become ready"
  curl -sv --max-time 5 "http://localhost:8083/healthz" || true
  echo
  curl -sv --max-time 5 "http://localhost:8083/readyz" || true
  echo
  kubectl get pods -n odigos-central -l app=central-backend || true
  kubectl logs -n odigos-central -l app=central-backend --tail=50 || true
  exit 1
fi

echo "🔐 Getting authentication token (with retry for up to 2 minutes while Keycloak starts up)..."
TOKEN=""
RETRY_COUNT=0
MAX_RETRIES=24  # 24 retries * 5 seconds = 2 minutes
RESPONSE=""

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
  echo "Attempt $((RETRY_COUNT + 1))/$MAX_RETRIES: Trying to authenticate with Keycloak..."

  # Try to get the token
  RESPONSE=$(curl -s -X POST "http://localhost:8082/realms/master/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=password" \
    -d "client_id=odigos-central" \
    -d "username=admin" \
    -d "password=admin" 2>/dev/null || true)

  # Check if we got a valid response with an access token
  if [ -n "$RESPONSE" ]; then
    TOKEN=$(echo "$RESPONSE" | jq -r '.access_token // empty' 2>/dev/null || true)

    if [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && [ "$TOKEN" != "empty" ]; then
      echo "✅ Authentication successful on attempt $((RETRY_COUNT + 1))"
      break
    else
      # Check for specific error messages to understand what's happening
      ERROR_MSG=$(echo "$RESPONSE" | jq -r '.error_description // .error // "Unknown error"' 2>/dev/null || echo "non-JSON response")
      echo "⚠️  Got response but no valid token: $ERROR_MSG"
    fi
  else
    echo "⚠️  No response from Keycloak (likely still starting up)"
  fi

  # If we got here, the attempt failed
  RETRY_COUNT=$((RETRY_COUNT + 1))

  if [ $RETRY_COUNT -lt $MAX_RETRIES ]; then
    echo "⏳ Waiting 5 seconds before retry..."
    sleep 5
  fi
done

if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ] || [ "$TOKEN" = "empty" ]; then
  echo "❌ Failed to get access token after $MAX_RETRIES attempts (2 minutes)"
  echo "Last response: $RESPONSE"
  echo "This usually means Keycloak is not ready or there's a configuration issue"
  exit 1
fi

echo "🏥 Testing central-backend GraphQL..."
SYSTEM_CONFIG=$(curl -s -X POST "http://localhost:8083/graphql" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"query": "{ systemConfig { version isInitialized } }"}' || true)

if ! echo "$SYSTEM_CONFIG" | jq -e . >/dev/null 2>&1; then
  echo "❌ Central-backend GraphQL returned non-JSON"
  echo "Response: $SYSTEM_CONFIG"
  exit 1
fi

SUCCESS=$(echo "$SYSTEM_CONFIG" | jq -r '.data.systemConfig.version // ""')
if [ -z "$SUCCESS" ]; then
  echo "❌ Central-backend GraphQL failed"
  echo "Response: $SYSTEM_CONFIG"
  exit 1
fi
echo "✅ Central-backend GraphQL working"

echo "🔍 Finding connected clusters..."
CLUSTERS=$(curl -s -X POST "http://localhost:8083/graphql" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"query": "{ computePlatforms { id name status } }"}')

PROXY_ID=$(echo "$CLUSTERS" | jq -r '.data.computePlatforms[0].id // ""')
CLUSTER_NAME=$(echo "$CLUSTERS" | jq -r '.data.computePlatforms[0].name // ""')

if [ -z "$PROXY_ID" ] || [ "$PROXY_ID" = "null" ]; then
  echo "⚠️ No connected clusters found - skipping remoteFetch test"
  exit 0
fi
echo "✅ Found connected cluster: $CLUSTER_NAME"

echo "📊 Testing namespace instrumentation via remoteFetch..."
MUTATION_RESULT=$(curl -s -X POST "http://localhost:8083/graphql" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "{
    \"query\": \"query RemoteFetch(\$proxyID: String!, \$query: String!, \$variables: JSON!) { remoteFetch(proxyID: \$proxyID, query: \$query, variables: \$variables) }\",
    \"variables\": {
      \"proxyID\": \"$PROXY_ID\",
      \"query\": \"mutation PersistNamespaces(\$namespaces: [PersistNamespaceItemInput!]!) { persistK8sNamespaces(namespaces: \$namespaces) }\",
      \"variables\": {
        \"namespaces\": [
          {
            \"namespace\": \"default\",
            \"selected\": true,
            \"currentStreamName\": \"\"
          }
        ]
      }
    }
  }")

# Parse nested response
NESTED_RESPONSE=$(echo "$MUTATION_RESULT" | jq -r '.data.remoteFetch // ""')
if [ -z "$NESTED_RESPONSE" ]; then
  echo "❌ RemoteFetch failed"
  echo "Response: $MUTATION_RESULT"
  exit 1
fi

PERSIST_SUCCESS=$(echo "$NESTED_RESPONSE" | jq -r '.data.persistK8sNamespaces // false')
if [ "$PERSIST_SUCCESS" != "true" ]; then
  echo "❌ Namespace instrumentation failed"
  echo "Nested response: $NESTED_RESPONSE"
  exit 1
fi

echo "✅ Namespace instrumentation successful!"

echo "🧹 Cleaning up: un-instrumenting namespace via remoteFetch..."
CLEANUP_RESULT=$(curl -s -X POST "http://localhost:8083/graphql" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "{
    \"query\": \"query RemoteFetch(\$proxyID: String!, \$query: String!, \$variables: JSON!) { remoteFetch(proxyID: \$proxyID, query: \$query, variables: \$variables) }\",
    \"variables\": {
      \"proxyID\": \"$PROXY_ID\",
      \"query\": \"mutation PersistNamespaces(\$namespaces: [PersistNamespaceItemInput!]!) { persistK8sNamespaces(namespaces: \$namespaces) }\",
      \"variables\": {
        \"namespaces\": [
          {
            \"namespace\": \"default\",
            \"selected\": false,
            \"currentStreamName\": \"\"
          }
        ]
      }
    }
  }")

CLEANUP_RESPONSE=$(echo "$CLEANUP_RESULT" | jq -r '.data.remoteFetch // ""')
CLEANUP_SUCCESS=$(echo "$CLEANUP_RESPONSE" | jq -r '.data.persistK8sNamespaces // false')
if [ "$CLEANUP_SUCCESS" = "true" ]; then
  echo "✅ Namespace un-instrumented successfully"
else
  echo "⚠️ Namespace un-instrumentation response: $CLEANUP_RESPONSE"
fi

echo "🧹 Deleting any remaining namespace-level Source CRs in default namespace..."
kubectl get sources -n default -o json | jq -r '.items[] | select(.spec.workload.kind == "Namespace") | .metadata.name' | while read -r src; do
  echo "  Deleting source/$src"
  kubectl delete source "$src" -n default --ignore-not-found
done

echo "🎉 Central GraphQL Integration Test Complete!"
