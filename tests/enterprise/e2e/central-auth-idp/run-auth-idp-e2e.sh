#!/usr/bin/env bash
# Standalone Central Auth IdP e2e (not part of the Cypress suite).
#
# Covers:
# 1) Live OIDC discovery for Azure/(Okta when E2E_OKTA_TENANT_ID is set)
#    via go test in central-backend/internal/keycloak (getOIDCDiscoveryEndpoints path)
# 2) Create IdP for AZURE/OKTA × OIDC/SAML (one at a time), resetting the
#    brokered Keycloak state between cases
# 3) Login using the created IdP:
#    - OIDC: full authorization-code login against in-cluster mock-oauth2-server
#    - SAML: broker login redirect reaches the configured SSO endpoint
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../../../.." && pwd)"
BACKEND_PORT="${BACKEND_PORT:-18084}"
KEYCLOAK_PORT="${KEYCLOAK_PORT:-18085}"
SAML_SSO_PORT="${SAML_SSO_PORT:-18086}"
NAMESPACE="odigos-central"
NODE_PORT_OIDC=30089

BACKEND_PID=""
KEYCLOAK_PID=""
SAML_SSO_PID=""
MOCK_OIDC_DISCOVERY=""

cleanup() {
  if [ -n "${BACKEND_PID}" ]; then kill "${BACKEND_PID}" 2>/dev/null || true; fi
  if [ -n "${KEYCLOAK_PID}" ]; then kill "${KEYCLOAK_PID}" 2>/dev/null || true; fi
  if [ -n "${SAML_SSO_PID}" ]; then kill "${SAML_SSO_PID}" 2>/dev/null || true; fi
}
trap cleanup EXIT

# Status messages go to stderr so command substitutions only capture return values.
log() { echo "▶ $*" >&2; }
fail() { echo "❌ $*" >&2; exit 1; }
pass() { echo "✅ $*" >&2; }

http_code() {
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 "$1" 2>/dev/null || true)
  [ -n "$code" ] || code="000"
  echo "$code"
}

start_port_forwards() {
  if [ -n "${BACKEND_PID}" ]; then kill "${BACKEND_PID}" 2>/dev/null || true; fi
  if [ -n "${KEYCLOAK_PID}" ]; then kill "${KEYCLOAK_PID}" 2>/dev/null || true; fi
  kubectl port-forward -n "$NAMESPACE" svc/central-backend "${BACKEND_PORT}:8081" >/dev/null 2>&1 &
  BACKEND_PID=$!
  kubectl port-forward -n "$NAMESPACE" svc/keycloak "${KEYCLOAK_PORT}:8080" >/dev/null 2>&1 &
  KEYCLOAK_PID=$!
  sleep 2
}

wait_backend_ready() {
  log "Waiting for central-backend /readyz"
  for _ in $(seq 1 90); do
    if ! kill -0 "${BACKEND_PID}" 2>/dev/null; then start_port_forwards; fi
    if [ "$(http_code "http://localhost:${BACKEND_PORT}/readyz")" = "200" ]; then
      pass "central-backend is ready"
      return 0
    fi
    sleep 2
  done
  fail "central-backend /readyz did not become ready"
}

ensure_port_forwards() {
  if ! kill -0 "${BACKEND_PID}" 2>/dev/null || ! kill -0 "${KEYCLOAK_PID}" 2>/dev/null; then
    start_port_forwards
  fi
}

keycloak_admin_token() {
  curl -sS -X POST "http://localhost:${KEYCLOAK_PORT}/realms/master/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "grant_type=password&client_id=admin-cli&username=admin&password=admin" \
    | jq -r '.access_token // empty'
}

keycloak_admin_get() {
  local path="$1" token="$2"
  curl -sS --max-time 30 "http://localhost:${KEYCLOAK_PORT}${path}" \
    -H "Authorization: Bearer ${token}" \
    -H "Content-Type: application/json"
}

keycloak_admin_delete() {
  local path="$1" token="$2" code
  code=$(curl -sS --max-time 30 -o /dev/null -w "%{http_code}" -X DELETE \
    "http://localhost:${KEYCLOAK_PORT}${path}" \
    -H "Authorization: Bearer ${token}")
  # 404 means someone else already removed it, which is the state we want.
  case "$code" in
    2*|404) return 0 ;;
    *) fail "DELETE ${path} returned HTTP ${code}" ;;
  esac
}

# central-backend creates the odigos-central client through ensureKeycloakState,
# which lands after /readyz starts answering, so /readyz alone is not enough.
wait_for_odigos_central_client() {
  log "Waiting for Keycloak client odigos-central"
  local token body
  for _ in $(seq 1 90); do
    if ! kill -0 "${KEYCLOAK_PID}" 2>/dev/null; then start_port_forwards; fi
    token="$(keycloak_admin_token)"
    if [ -n "$token" ] && [ "$token" != "null" ]; then
      body=$(curl -sS "http://localhost:${KEYCLOAK_PORT}/admin/realms/master/clients?clientId=odigos-central" \
        -H "Authorization: Bearer ${token}" || true)
      if echo "$body" | jq -e 'type=="array" and length>0 and any(.[]; .clientId=="odigos-central")' >/dev/null 2>&1; then
        pass "odigos-central client is present"
        return 0
      fi
    fi
    sleep 2
  done
  fail "odigos-central client is missing from Keycloak"
}

# Delete every IdP instance plus the users the broker created for them. This is
# the only Keycloak state the matrix carries between cases, so wiping it through
# the admin API leaves the realm equivalent to a fresh H2 store without paying
# for two rollouts per case.
reset_keycloak_idp_state() {
  log "Resetting Keycloak IdP state via the admin API"
  ensure_port_forwards

  local token idps users alias user_id username
  token="$(keycloak_admin_token)"
  if [ -z "$token" ] || [ "$token" = "null" ]; then
    start_port_forwards
    token="$(keycloak_admin_token)"
  fi
  [ -n "$token" ] && [ "$token" != "null" ] || fail "failed to get Keycloak admin token for IdP reset"

  # Brokered users first: deleting an IdP also drops its federated-identity
  # links, which would leave the users below indistinguishable from local ones.
  # A leftover brokered user makes the next login hit Keycloak's "account
  # already exists" interstitial instead of the first-broker-login path this
  # test covers. Everything in this realm is either the bootstrap admin, a
  # client service account, or a user some IdP brokered in.
  users="$(keycloak_admin_get "/admin/realms/master/users?briefRepresentation=true&max=1000" "$token")"
  echo "$users" | jq -e 'type=="array"' >/dev/null 2>&1 \
    || fail "unexpected users list response: ${users}"

  while read -r user_id username; do
    [ -n "$user_id" ] || continue
    keycloak_admin_delete "/admin/realms/master/users/${user_id}" "$token"
    log "Deleted brokered user ${username}"
  done < <(echo "$users" | jq -r '
    .[]
    | select(.username != "admin" and (.username | startswith("service-account-") | not))
    | "\(.id) \(.username)"')

  idps="$(keycloak_admin_get "/admin/realms/master/identity-provider/instances" "$token")"
  echo "$idps" | jq -e 'type=="array"' >/dev/null 2>&1 \
    || fail "unexpected identity-provider list response: ${idps}"

  while read -r alias; do
    [ -n "$alias" ] || continue
    keycloak_admin_delete "/admin/realms/master/identity-provider/instances/${alias}" "$token"
    log "Deleted IdP ${alias}"
  done < <(echo "$idps" | jq -r '.[].alias // empty')

  pass "Keycloak IdP state reset"
}

graphql() {
  local query="$1"
  curl -sS -X POST "http://localhost:${BACKEND_PORT}/graphql" \
    -H "Content-Type: application/json" \
    -d "$(jq -nc --arg q "$query" '{query:$q}')"
}

kind_node_ip() {
  docker inspect kind-control-plane --format '{{.NetworkSettings.Networks.kind.IPAddress}}' 2>/dev/null \
    || kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}'
}

ensure_mock_oidc() {
  log "Applying mock OIDC IdP"
  kubectl apply -f "$(dirname "$0")/manifests/mock-oidc.yaml"
  kubectl rollout status deployment/mock-oidc -n "$NAMESPACE" --timeout=3m

  local node_ip
  node_ip="$(kind_node_ip)"
  [ -n "$node_ip" ] || fail "could not resolve kind node IP"
  MOCK_OIDC_DISCOVERY="http://${node_ip}:${NODE_PORT_OIDC}/default/.well-known/openid-configuration"

  log "Waiting for mock OIDC discovery at ${MOCK_OIDC_DISCOVERY}"
  for _ in $(seq 1 60); do
    if [ "$(http_code "$MOCK_OIDC_DISCOVERY")" = "200" ]; then
      pass "mock OIDC discovery is reachable"
      return 0
    fi
    sleep 2
  done
  fail "mock OIDC discovery not reachable"
}

start_saml_sso_stub() {
  if [ -n "${SAML_SSO_PID}" ] && kill -0 "${SAML_SSO_PID}" 2>/dev/null; then
    return 0
  fi
  log "Starting local SAML SSO stub on :${SAML_SSO_PORT}"
  python3 - <<PY >/tmp/saml-sso-stub.log 2>&1 &
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(b"<html><body>mock-saml-sso</body></html>")
    def log_message(self, fmt, *args):
        return

HTTPServer(("0.0.0.0", ${SAML_SSO_PORT}), Handler).serve_forever()
PY
  SAML_SSO_PID=$!
  for _ in $(seq 1 20); do
    if [ "$(http_code "http://127.0.0.1:${SAML_SSO_PORT}/sso")" = "200" ]; then
      pass "SAML SSO stub is up"
      return 0
    fi
    sleep 0.5
  done
  fail "SAML SSO stub failed to start"
}

gen_saml_cert() {
  local dir
  dir="$(mktemp -d)"
  openssl req -x509 -newkey rsa:2048 \
    -keyout "$dir/key.pem" -out "$dir/cert.pem" \
    -days 1 -nodes -subj "/CN=central-auth-idp-e2e" >/dev/null 2>&1
  openssl x509 -in "$dir/cert.pem" -outform DER 2>/dev/null | base64 -w0
}

assert_idp_exists() {
  local alias="$1"
  local token body
  token="$(keycloak_admin_token)"
  [ -n "$token" ] && [ "$token" != "null" ] || fail "failed to get Keycloak admin token"

  body=$(curl -sS "http://localhost:${KEYCLOAK_PORT}/admin/realms/master/identity-provider/instances" \
    -H "Authorization: Bearer ${token}")
  echo "$body" | jq -e --arg a "$alias" 'map(.alias) | index($a) != null' >/dev/null \
    || fail "IdP alias ${alias} not found in Keycloak: $body"
  pass "Keycloak has IdP alias ${alias}"
}

create_oidc_idp() {
  local provider="$1"
  local discovery="$2"
  local tenant="$3"
  local query body success
  query=$(cat <<EOF
mutation {
  ensureOidcIdentityProvider(input: {
    providerId: ${provider}
    clientId: "e2e-${provider}-client"
    clientSecret: "e2e-${provider}-secret"
    discoveryUrl: "${discovery}"
    tenantId: "${tenant}"
  }) { success callbackUrl error }
}
EOF
)
  body=$(graphql "$query")
  success=$(echo "$body" | jq -r '.data.ensureOidcIdentityProvider.success // false')
  [ "$success" = "true" ] || fail "OIDC create ${provider} failed: $body"
  pass "Created OIDC IdP for ${provider} (discovery=${discovery})"
}

create_saml_idp() {
  local provider="$1"
  local entity_id="$2"
  local sso_url="$3"
  local cert_b64="$4"
  local query body success
  query=$(cat <<EOF
mutation {
  ensureSamlIdentityProvider(input: {
    providerId: ${provider}
    idpEntityId: "${entity_id}"
    singleSignOnServiceUrl: "${sso_url}"
    signingCertificate: "${cert_b64}"
  }) { success callbackUrl error }
}
EOF
)
  body=$(graphql "$query")
  success=$(echo "$body" | jq -r '.data.ensureSamlIdentityProvider.success // false')
  [ "$success" = "true" ] || fail "SAML create ${provider} failed: $body"
  pass "Created SAML IdP for ${provider}"
}

assert_signin_url() {
  local provider="$1"
  local body url
  body=$(graphql "{ signInUrl(providerId: ${provider}) }")
  url=$(echo "$body" | jq -r '.data.signInUrl // empty')
  [ -n "$url" ] || fail "signInUrl empty for ${provider}: $body"
  pass "signInUrl for ${provider} is set"
  echo "$url"
}

# Rewrite Keycloak/browser URLs so redirects through the local port-forward work.
# Chart Keycloak advertises hostname localhost:8081 (central-backend proxy port).
rewrite_local_auth_url() {
  local u="$1"
  # Resolve Keycloak reverse-proxy relative Location headers against the backend.
  if [[ "$u" == /* ]]; then
    u="http://localhost:${BACKEND_PORT}${u}"
  fi
  u="$(echo "$u" | sed -E \
    -e "s#https?://localhost:8081/#http://localhost:${BACKEND_PORT}/#g" \
    -e "s#https?://127\.0\.0\.1:8081/#http://localhost:${BACKEND_PORT}/#g" \
    -e "s#https://localhost:#http://localhost:#g" \
    -e "s#https://127\.0\.0\.1:#http://127.0.0.1:#g")"
  echo "$u"
}

# Return the app-facing OAuth code (Keycloak → central-ui), never the IdP→Keycloak
# broker callback code on /broker/{alias}/endpoint.
extract_oauth_code() {
  URL="$1" NEXT="$2" python3 - <<'PY'
import os, urllib.parse

def app_code(candidate: str) -> str:
    if not candidate:
        return ""
    parsed = urllib.parse.urlparse(candidate)
    path = parsed.path or ""
    # IdP posts/redirects here; Keycloak still needs to exchange this code.
    if "/broker/" in path and path.rstrip("/").endswith("/endpoint"):
        return ""
    q = urllib.parse.parse_qs(parsed.query)
    codes = q.get("code") or []
    return codes[0] if codes else ""

for candidate in (os.environ.get("NEXT") or "", os.environ.get("URL") or ""):
    code = app_code(candidate)
    if code:
        print(code)
        break
PY
}

# If mock-oauth2-server shows its login form, POST a username/claims to continue.
submit_mock_oidc_login() {
  local authorize_url="$1"
  local cookie_jar="$2"
  local claims='{"sub":"e2e-user","email":"e2e-user@example.com","email_verified":true,"preferred_username":"e2e-user","given_name":"E2E","family_name":"User"}'
  curl -sS -k -c "$cookie_jar" -b "$cookie_jar" -o /tmp/oidc-login-body.txt -D /tmp/oidc-login-headers.txt \
    -w "%{redirect_url}" --max-redirs 0 \
    -X POST "$authorize_url" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    --data-urlencode "username=e2e-user" \
    --data-urlencode "claims=${claims}" || true
}

# Submit Keycloak HTML forms (first-broker-login / update profile) by POSTing inputs.
submit_html_form() {
  local page_url="$1"
  local cookie_jar="$2"
  PAGE_URL="$page_url" COOKIE_JAR="$cookie_jar" BACKEND_PORT="$BACKEND_PORT" python3 - <<'PY'
import os, re, subprocess, urllib.parse
from html.parser import HTMLParser

html = open("/tmp/oidc-login-body.txt").read()
page_url = os.environ["PAGE_URL"]
cookie_jar = os.environ["COOKIE_JAR"]
backend_port = os.environ["BACKEND_PORT"]

class FormParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.forms = []
        self._cur = None
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "form":
            self._cur = {"action": a.get("action", ""), "method": a.get("method", "post"), "inputs": []}
            self.forms.append(self._cur)
        elif tag == "input" and self._cur is not None:
            name = a.get("name")
            if not name:
                return
            typ = (a.get("type") or "text").lower()
            if typ in ("checkbox", "radio") and "checked" not in a:
                return
            if typ in ("button", "reset"):
                return
            self._cur["inputs"].append((name, a.get("value", "")))
        elif tag == "button" and self._cur is not None:
            name = a.get("name")
            typ = (a.get("type") or "submit").lower()
            if name and typ == "submit":
                self._cur["inputs"].append((name, a.get("value", "")))

p = FormParser()
p.feed(html)
form = next((f for f in p.forms if f["action"] or f["inputs"]), None)
if not form:
    raise SystemExit(0)

action = form["action"] or page_url
action = urllib.parse.urljoin(page_url, action)
if action.startswith("/"):
    action = f"http://localhost:{backend_port}{action}"
action = re.sub(r"https?://localhost:8081/", f"http://localhost:{backend_port}/", action)
action = re.sub(r"https://localhost:", "http://localhost:", action)

# Ensure profile fields are filled if the review-profile form is empty.
values = {k: v for k, v in form["inputs"]}
defaults = {
    "email": "e2e-user@example.com",
    "username": "e2e-user",
    "firstName": "E2E",
    "lastName": "User",
}
for k, v in defaults.items():
    if k in values and not values[k]:
        values[k] = v
if "submitAction" not in values and "save" not in values:
    values["submitAction"] = "Submit"

data = urllib.parse.urlencode(values)
cmd = [
    "curl", "-sS", "-k",
    "-c", cookie_jar, "-b", cookie_jar,
    "-o", "/tmp/oidc-login-body.txt",
    "-D", "/tmp/oidc-login-headers.txt",
    "-w", "%{redirect_url}",
    "--max-redirs", "0",
    "-X", "POST", action,
    "-H", "Content-Type: application/x-www-form-urlencoded",
    "--data", data,
]
out = subprocess.check_output(cmd, text=True)
print(out.strip(), end="")
PY
}

login_oidc_mock() {
  local provider="$1"
  local signin_url="$2"
  local cookie_jar url next code body status i

  cookie_jar="$(mktemp)"
  url="$(rewrite_local_auth_url "$signin_url")"
  code=""
  log "OIDC login start for ${provider}: ${url}"

  # Follow broker redirects manually so we can capture ?code= even if CENTRAL_UI_HOST is unreachable.
  for i in $(seq 1 25); do
    next=$(curl -sS -k -c "$cookie_jar" -b "$cookie_jar" -o /tmp/oidc-login-body.txt -D /tmp/oidc-login-headers.txt \
      -w "%{redirect_url}" --max-redirs 0 "$url" || true)

    if [ -z "$next" ]; then
      next=$(tr -d '\r' </tmp/oidc-login-headers.txt | awk 'tolower($1)=="location:"{print $2; exit}')
    fi
    next="$(rewrite_local_auth_url "$next")"

    code="$(extract_oauth_code "$url" "$next")"
    if [ -n "$code" ]; then
      break
    fi

    # mock-oauth2-server interactive login page (or residual if JSON_CONFIG is ignored).
    if [ -z "$next" ] && grep -q 'mock-oauth2-server' /tmp/oidc-login-body.txt 2>/dev/null; then
      log "Submitting mock OIDC login form for ${provider}"
      next="$(submit_mock_oidc_login "$url" "$cookie_jar")"
      if [ -z "$next" ]; then
        next=$(tr -d '\r' </tmp/oidc-login-headers.txt | awk 'tolower($1)=="location:"{print $2; exit}')
      fi
      next="$(rewrite_local_auth_url "$next")"
      code="$(extract_oauth_code "$url" "$next")"
      if [ -n "$code" ]; then
        break
      fi
      if [ -n "$next" ]; then
        url="$next"
        continue
      fi
    fi

    # Keycloak first-broker-login / VERIFY_PROFILE interstitials (if setup did not disable them).
    needs_login_action=0
    if [ -z "$next" ]; then
      if echo "$url" | grep -Eq 'first-broker-login|required-action'; then
        needs_login_action=1
      elif grep -Eiq 'kc-update-profile-form|Update Account Information|VERIFY_PROFILE' /tmp/oidc-login-body.txt 2>/dev/null; then
        needs_login_action=1
      fi
    fi
    if [ "$needs_login_action" = "1" ]; then
      log "Submitting Keycloak login-action form for ${provider} (${url})"
      next="$(submit_html_form "$url" "$cookie_jar")"
      if [ -z "$next" ]; then
        next=$(tr -d '\r' </tmp/oidc-login-headers.txt | awk 'tolower($1)=="location:"{print $2; exit}')
      fi
      next="$(rewrite_local_auth_url "$next")"
      code="$(extract_oauth_code "$url" "$next")"
      if [ -n "$code" ]; then
        break
      fi
      if [ -n "$next" ]; then
        url="$next"
        continue
      fi
      # Form returned 200 with another step; continue on refreshed body / same URL.
      continue
    fi

    # Some IdPs return 200 HTML auto-post with the app code embedded.
    if [ -z "$next" ] && grep -q 'name="code"' /tmp/oidc-login-body.txt 2>/dev/null; then
      code=$(python3 - <<'PY'
import re
html=open("/tmp/oidc-login-body.txt").read()
m=re.search(r'name="code"\s+value="([^"]+)"', html)
print(m.group(1) if m else "")
PY
)
      [ -n "$code" ] && break
    fi

    [ -n "$next" ] || break
    url="$next"
  done

  if [ -z "$code" ]; then
    log "OIDC login debug headers:"; tr -d '\r' </tmp/oidc-login-headers.txt | head -40 >&2 || true
    log "OIDC login debug body:"; head -c 500 /tmp/oidc-login-body.txt >&2 || true
    fail "OIDC login for ${provider} did not return an authorization code (last_url=${url})"
  fi

  body=$(graphql "mutation { signIn(code: \"${code}\") { status user { email } } }")
  status=$(echo "$body" | jq -r '.data.signIn.status // empty')
  [ "$status" = "SUCCESS" ] || [ "$status" = "PASSWORD_UPDATE_REQUIRED" ] \
    || fail "OIDC signIn for ${provider} failed: $body"
  pass "OIDC login succeeded for ${provider} (status=${status})"
}

assert_saml_login_redirect() {
  local provider="$1"
  local signin_url="$2"
  local sso_url="$3"
  local cookie_jar url next i reached=0

  cookie_jar="$(mktemp)"
  url="$(rewrite_local_auth_url "$signin_url")"
  log "SAML login start for ${provider}: ${url}"

  # Prefer GET over HEAD: Keycloak auth endpoints often ignore HEAD and return 200
  # without a Location, which made the previous -I check fail spuriously.
  for i in $(seq 1 15); do
    next=$(curl -sS -k -c "$cookie_jar" -b "$cookie_jar" -o /tmp/saml-login-body.txt -D /tmp/saml-login-headers.txt \
      -w "%{redirect_url}" --max-redirs 0 "$url" || true)
    if [ -z "$next" ]; then
      next=$(tr -d '\r' </tmp/saml-login-headers.txt | awk 'tolower($1)=="location:"{print $2; exit}')
    fi
    next="$(rewrite_local_auth_url "$next")"

    if echo "$url$next" | grep -Eq "127\\.0\\.0\\.1:${SAML_SSO_PORT}|localhost:${SAML_SSO_PORT}" \
      || grep -Fq "mock-saml-sso" /tmp/saml-login-body.txt 2>/dev/null; then
      reached=1
      break
    fi

    # SAML redirect-binding HTML auto-post to the IdP SSO URL.
    if [ -z "$next" ] && grep -Eiq 'SAMLRequest|saml-post-form|action=' /tmp/saml-login-body.txt 2>/dev/null; then
      next=$(python3 - <<'PY'
import re
from html.parser import HTMLParser
html=open("/tmp/saml-login-body.txt").read()
class P(HTMLParser):
    action=""
    def handle_starttag(self, tag, attrs):
        if tag=="form" and not self.action:
            self.action=dict(attrs).get("action","")
p=P(); p.feed(html)
print(p.action or "")
PY
)
      next="$(rewrite_local_auth_url "$next")"
    fi

    [ -n "$next" ] || break
    url="$next"
  done

  [ "$reached" = "1" ] \
    || fail "SAML login for ${provider} did not reach SSO stub (last_url=${url}, sso=${sso_url})"
  pass "SAML login redirect reached IdP SSO for ${provider}"
}

# ---------------------------
# Phase 0: live discovery unit tests
# ---------------------------
log "Phase 0: live OIDC discovery tests (getOIDCDiscoveryEndpoints)"
(
  cd "${ROOT_DIR}/central-backend"
  go test ./internal/keycloak/ -count=1 -run 'TestResolveDiscoveryURL|TestGetOIDCDiscoveryEndpoints_|TestGetIDPConfig_PopulatesEndpointsFromLiveDiscovery' -timeout 2m
)
pass "Discovery tests passed"

# ---------------------------
# Phase 1+2: create / login matrix
# ---------------------------
start_port_forwards
wait_backend_ready
wait_for_odigos_central_client
ensure_mock_oidc
start_saml_sso_stub

PROVIDERS=(AZURE OKTA)
SAML_CERT_B64="$(gen_saml_cert)"
SAML_ENTITY_ID="http://127.0.0.1:${SAML_SSO_PORT}/metadata"
SAML_SSO_URL="http://127.0.0.1:${SAML_SSO_PORT}/sso"

GOOGLE_DISCOVERY="https://accounts.google.com/.well-known/openid-configuration"
AZURE_DISCOVERY="https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration"
AZURE_TENANT="${E2E_AZURE_TENANT_ID:-00000000-0000-0000-0000-000000000001}"
OKTA_TENANT="${E2E_OKTA_TENANT_ID:-}"

for provider in "${PROVIDERS[@]}"; do
  for protocol in OIDC SAML; do
    echo
    log "======== ${provider} / ${protocol} ========"
    reset_keycloak_idp_state

    alias="$(echo "$provider" | tr '[:upper:]' '[:lower:]')"

    if [ "$protocol" = "OIDC" ]; then
      discovery="$MOCK_OIDC_DISCOVERY"
      tenant="e2e-tenant"

      # Create with live vendor discovery first so admin.go getOIDCDiscoveryEndpoints
      # fetches Google/Azure/(Okta) metadata during IdP ensure.
      if [ "${DISCOVERY_MODE:-live}" = "live" ]; then
        case "$provider" in
          GOOGLE) discovery="$GOOGLE_DISCOVERY"; tenant="" ;; # Google discovery has no {tenantid}
          AZURE) discovery="$AZURE_DISCOVERY"; tenant="$AZURE_TENANT" ;;
          OKTA)
            if [ -n "$OKTA_TENANT" ]; then
              discovery="https://${OKTA_TENANT}.okta.com/.well-known/openid-configuration"
              tenant="$OKTA_TENANT"
            else
              log "E2E_OKTA_TENANT_ID unset — using mock discovery for Okta OIDC create"
              discovery="$MOCK_OIDC_DISCOVERY"
            fi
            ;;
        esac
      fi

      create_oidc_idp "$provider" "$discovery" "$tenant"
      assert_idp_exists "$alias"
      assert_signin_url "$provider" >/dev/null

      if [ "$discovery" != "$MOCK_OIDC_DISCOVERY" ]; then
        log "Updating ${provider} OIDC IdP to mock discovery for login coverage"
        create_oidc_idp "$provider" "$MOCK_OIDC_DISCOVERY" "e2e-tenant"
      fi
      signin_url="$(assert_signin_url "$provider")"
      # Browser-facing Keycloak URLs are reverse-proxied through central-backend.
      signin_url="$(rewrite_local_auth_url "$signin_url")"
      signin_url="$(echo "$signin_url" | sed -E "s#https?://[^/]+#http://localhost:${BACKEND_PORT}#")"
      login_oidc_mock "$provider" "$signin_url"
    else
      create_saml_idp "$provider" "$SAML_ENTITY_ID" "$SAML_SSO_URL" "$SAML_CERT_B64"
      assert_idp_exists "$alias"
      signin_url="$(assert_signin_url "$provider")"
      signin_url="$(rewrite_local_auth_url "$signin_url")"
      signin_url="$(echo "$signin_url" | sed -E "s#https?://[^/]+#http://localhost:${BACKEND_PORT}#")"
      assert_saml_login_redirect "$provider" "$signin_url" "$SAML_SSO_URL"
    fi
  done
done

echo
pass "All central-auth-idp e2e cases passed"
