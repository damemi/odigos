#!/usr/bin/env bash
# Helm template smoke test for Aiden resources (placeholder keys in a temp values file).
set -euo pipefail

P="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
values_file="$(mktemp)"
rendered="$(mktemp)"
trap 'rm -f "$values_file" "$rendered"' EXIT

cat >"$values_file" <<'EOF'
onPremToken: test-token
aiden:
  enabled: true
  gemini:
    key: placeholder-gemini-key
  jaeger:
    endpoints:
      default: http://jaeger-query.tracing.svc:16686
EOF

# Slack is optional: in-UI chat is the default path when Aiden is enabled.
helm template odigos "$P/helm/odigos" \
  --namespace odigos-system \
  -f "$values_file" \
  --show-only templates/aiden/configmap.yaml \
  --show-only templates/aiden/deployment.yaml \
  --show-only templates/aiden/service.yaml \
  --show-only templates/aiden/secret.yaml \
  --show-only templates/ui/deployment.yaml \
  >"$rendered"

grep -q 'jaeger-config.json' "$rendered"
grep -q 'JAEGER_SKILL_CONFIG' "$rendered"
grep -q '/app/aiden-skills' "$rendered"
grep -q '/app/aiden/' "$rendered"
grep -q 'AGENTS.md' "$rendered"
grep -q 'OPENCLAW_GATEWAY_TOKEN' "$rendered"
grep -q 'AIDEN_GATEWAY_URL' "$rendered"
grep -q 'kind: Service' "$rendered"
grep -q 'name: odigos-aiden' "$rendered"
grep -q '"bind": "lan"' "$rendered"
# Slack channel must not be configured when tokens are omitted.
if grep -q '"slack"' "$rendered"; then
  echo "expected no Slack channel when slack tokens are empty" >&2
  exit 1
fi
if grep -q 'SLACK_APP_TOKEN' "$rendered"; then
  echo "expected no Slack env when slack tokens are empty" >&2
  exit 1
fi

cat >"$values_file" <<'EOF'
onPremToken: test-token
aiden:
  enabled: true
  gemini:
    key: placeholder-gemini-key
  slack:
    key: xapp-placeholder
    botToken: xoxb-placeholder
  jaeger:
    endpoints:
      default: http://jaeger-query.tracing.svc:16686
EOF

helm template odigos "$P/helm/odigos" \
  --namespace odigos-system \
  -f "$values_file" \
  --show-only templates/aiden/configmap.yaml \
  --show-only templates/aiden/deployment.yaml \
  >"$rendered"

grep -q 'jaeger-config.json' "$rendered"
grep -q 'SLACK_APP_TOKEN' "$rendered"
grep -q '"slack"' "$rendered"

echo "Aiden helm template checks passed"
