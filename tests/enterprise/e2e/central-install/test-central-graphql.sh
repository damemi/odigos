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

echo "🔐 Getting authentication token (with retry for up to 2 minutes while Keycloak starts up)..."
TOKEN=""
RETRY_COUNT=0
MAX_RETRIES=24  # 24 retries * 5 seconds = 2 minutes

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
  echo "Attempt $((RETRY_COUNT + 1))/$MAX_RETRIES: Trying to authenticate with Keycloak..."

  # Try to get the token
  RESPONSE=$(curl -s -X POST "http://localhost:8082/realms/master/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=password" \
    -d "client_id=odigos-central" \
    -d "username=admin" \
    -d "password=admin" 2>/dev/null)

  # Check if we got a valid response with an access token
  if [ -n "$RESPONSE" ]; then
    TOKEN=$(echo "$RESPONSE" | jq -r '.access_token // empty' 2>/dev/null)

    if [ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] && [ "$TOKEN" != "empty" ]; then
      echo "✅ Authentication successful on attempt $((RETRY_COUNT + 1))"
      break
    else
      # Check for specific error messages to understand what's happening
      ERROR_MSG=$(echo "$RESPONSE" | jq -r '.error_description // .error // "Unknown error"' 2>/dev/null)
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
  -d '{"query": "{ systemConfig { version isInitialized } }"}')

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
echo "🎉 Central GraphQL Integration Test Complete!"
