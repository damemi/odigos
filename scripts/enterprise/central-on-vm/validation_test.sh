#!/usr/bin/env bash
# Unit tests for the Central-on-VM installer/bundler/uninstaller guards.
# These scripts wipe K3s data, pick a released Helm chart, and serialize
# concurrent bundle builds — a loosened path check or a stale lock that
# is treated as live (or the reverse) is a host-wide regression.
#
# Functions are extracted from the production scripts so the tests pin the
# real implementations, not a copy.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL="${SCRIPT_DIR}/install-central-single-node.sh"
BUNDLE="${SCRIPT_DIR}/bundle-central-offline.sh"
UNINSTALL="${SCRIPT_DIR}/uninstall-central-single-node.sh"

PASS=0
FAIL=0

fail() {
  FAIL=$((FAIL + 1))
  printf 'FAIL: %s\n' "$*" >&2
}

pass() {
  PASS=$((PASS + 1))
}

expect_ok() {
  local label="$1"
  shift
  if "$@"; then
    pass
  else
    fail "${label}: expected success"
  fi
}

expect_fail() {
  local label="$1"
  shift
  if "$@"; then
    fail "${label}: expected failure"
  else
    pass
  fi
}

# die() exits the process, so callers that should reject must run in a subshell.
expect_die() {
  local label="$1"
  shift
  if ( "$@" ) >/dev/null 2>&1; then
    fail "${label}: expected die/exit"
  else
    pass
  fi
}

expect_eq() {
  local label="$1" got="$2" want="$3"
  if [[ "$got" == "$want" ]]; then
    pass
  else
    fail "${label}: got ${got@Q}, want ${want@Q}"
  fi
}

extract_fn() {
  local file="$1"
  local name="$2"
  awk -v name="$name" '
    $0 ~ "^" name "\\(\\) \\{" { grab = 1 }
    grab { print }
    grab && $0 == "}" { grab = 0 }
  ' "$file"
}

[[ -f "$INSTALL" && -f "$BUNDLE" && -f "$UNINSTALL" ]] || {
  printf 'production scripts missing under %s\n' "$SCRIPT_DIR" >&2
  exit 1
}

# Shared helpers used by the extracted functions.
eval "$(extract_fn "$INSTALL" die)"
eval "$(extract_fn "$INSTALL" validate_dns_name)"
eval "$(extract_fn "$INSTALL" validate_path)"
eval "$(extract_fn "$INSTALL" normalize_arch)"
eval "$(extract_fn "$UNINSTALL" read_state_value)"
eval "$(extract_fn "$UNINSTALL" validate_managed_data_dir)"
eval "$(extract_fn "$BUNDLE" validate_version)"
eval "$(extract_fn "$BUNDLE" chart_image)"
eval "$(extract_fn "$BUNDLE" sha256_file)"
eval "$(extract_fn "$BUNDLE" file_size)"
eval "$(extract_fn "$BUNDLE" log)"
eval "$(extract_fn "$BUNDLE" acquire_lock)"

# Bundle's normalize_arch has a different error string; keep it as
# bundle_normalize_arch so both implementations stay pinned.
eval "$(extract_fn "$BUNDLE" normalize_arch | sed '1s/^normalize_arch/bundle_normalize_arch/')"

# ---------------------------------------------------------------------------
# validate_dns_name — hostname is the TLS ingress SNI and Keycloak external URL
# ---------------------------------------------------------------------------
expect_ok "fqdn" validate_dns_name "central.example.com"
expect_ok "multi-label fqdn" validate_dns_name "a.b.example.co.uk"
expect_ok "digits in labels" validate_dns_name "c1.prod.example.com"
expect_fail "empty" validate_dns_name ""
expect_fail "single label" validate_dns_name "localhost"
expect_fail "adjacent dots" validate_dns_name "central..example.com"
expect_fail "leading hyphen" validate_dns_name "-central.example.com"
expect_fail "trailing hyphen" validate_dns_name "central.example.com-"
expect_fail "leading dot" validate_dns_name ".example.com"
expect_fail "trailing dot" validate_dns_name "central.example.com."
expect_fail "space" validate_dns_name "central example.com"
expect_fail "url scheme" validate_dns_name "https://central.example.com"
too_long="$(python3 -c 'print("a"*252 + ".b")')"
expect_fail "over 253 chars" validate_dns_name "$too_long"

# ---------------------------------------------------------------------------
# validate_path — installer refuses relative / traversal paths for secrets
# ---------------------------------------------------------------------------
expect_ok "abs file" validate_path "/secure/fullchain.pem"
expect_ok "nested abs" validate_path "/opt/odigos-central-v1.36.0-linux-amd64.tar.gz"
expect_ok "plus in name" validate_path "/var/tmp/k3s+airgap.tar"
expect_fail "relative" validate_path "secure/fullchain.pem"
expect_fail "empty" validate_path ""
expect_fail "dotdot component" validate_path "/secure/../etc/passwd"
expect_fail "dot component" validate_path "/secure/./token"
expect_fail "double slash" validate_path "/secure//token"
expect_fail "home shorthand" validate_path "~/token"
expect_fail "newline" validate_path $'/tmp/foo\n/etc/passwd'

# ---------------------------------------------------------------------------
# validate_managed_data_dir — uninstall does rm -rf on this path
# ---------------------------------------------------------------------------
expect_ok "default k3s data dir" validate_managed_data_dir "/var/lib/rancher/k3s"
expect_ok "custom deep path" validate_managed_data_dir "/opt/odigos/k3s-data"
expect_fail "root" validate_managed_data_dir "/"
expect_fail "var" validate_managed_data_dir "/var"
expect_fail "var lib" validate_managed_data_dir "/var/lib"
expect_fail "etc" validate_managed_data_dir "/etc"
expect_fail "usr" validate_managed_data_dir "/usr"
expect_fail "tmp" validate_managed_data_dir "/tmp"
expect_fail "opt (too shallow)" validate_managed_data_dir "/opt/odigos"
expect_fail "relative" validate_managed_data_dir "var/lib/rancher/k3s"
expect_fail "traversal" validate_managed_data_dir "/var/lib/rancher/k3s/../.."
expect_fail "dot component" validate_managed_data_dir "/var/lib/./rancher"
expect_fail "double slash" validate_managed_data_dir "/var/lib//rancher/k3s"

# ---------------------------------------------------------------------------
# normalize_arch
# ---------------------------------------------------------------------------
expect_eq "install x86_64" "$(normalize_arch x86_64)" "amd64"
expect_eq "install amd64" "$(normalize_arch amd64)" "amd64"
expect_eq "install aarch64" "$(normalize_arch aarch64)" "arm64"
expect_eq "install arm64" "$(normalize_arch arm64)" "arm64"
expect_die "install ppc64" normalize_arch ppc64le
expect_eq "bundle x86_64" "$(bundle_normalize_arch x86_64)" "amd64"
expect_eq "bundle aarch64" "$(bundle_normalize_arch aarch64)" "arm64"
expect_die "bundle riscv" bundle_normalize_arch riscv64

# ---------------------------------------------------------------------------
# validate_version — interpolated into URLs and filenames
# ---------------------------------------------------------------------------
expect_ok "central tag" validate_version "v1.36.0" "Central version"
expect_ok "k3s plus" validate_version "v1.33.3+k3s1" "K3s version"
expect_ok "helm" validate_version "v4.2.3" "Helm version"
expect_die "empty version" validate_version "" "Central version"
expect_die "path version" validate_version "../v1.36.0" "Central version"
expect_die "url version" validate_version "https://evil.example/v1" "Central version"
expect_die "space version" validate_version "v1.36.0 rc1" "Central version"

# ---------------------------------------------------------------------------
# read_state_value — uninstall rm -rf target is parsed from this file
# ---------------------------------------------------------------------------
MARKER_FILE="$(mktemp)"
cat > "$MARKER_FILE" <<'EOF'
STATUS='ready'
CENTRAL_VERSION='v1.36.0'
NAMESPACE='odigos-central'
DATA_DIR='/var/lib/rancher/k3s'
KUBECTL_USER='ubuntu'
IMAGE_PREFIX='registry.odigos.io'
# DATA_DIR='/tmp'
DATA_DIR_UNQUOTED=/evil
EOF
expect_eq "status" "$(read_state_value STATUS)" "ready"
expect_eq "data dir" "$(read_state_value DATA_DIR)" "/var/lib/rancher/k3s"
expect_eq "namespace" "$(read_state_value NAMESPACE)" "odigos-central"
expect_eq "kubectl user" "$(read_state_value KUBECTL_USER)" "ubuntu"
# A commented-out DATA_DIR must not be returned alongside (or instead of) the real key.
expect_eq "commented DATA_DIR is ignored" "$(read_state_value DATA_DIR)" "/var/lib/rancher/k3s"
expect_eq "unquoted ignored" "$(read_state_value DATA_DIR_UNQUOTED)" ""
expect_eq "missing key" "$(read_state_value NOT_A_KEY)" ""
rm -f "$MARKER_FILE"

# Lines that are not exactly KEY='value' fail closed (empty), so a trailing
# injection cannot be parsed as a prefix path that uninstall would rm -rf.
MARKER_FILE="$(mktemp)"
printf "%s\n" "DATA_DIR='/var/lib/rancher/k3s'; rm -rf /'" > "$MARKER_FILE"
expect_eq "tampered line fails closed" "$(read_state_value DATA_DIR)" ""
rm -f "$MARKER_FILE"

# ---------------------------------------------------------------------------
# chart_image — wrong section would pull Keycloak as Redis (or skip an image)
# ---------------------------------------------------------------------------
chart_tmp="$(mktemp -d)"
cat > "${chart_tmp}/values.yaml" <<'EOF'
image:
  tag: v1.36.0
imagePrefix: registry.odigos.io
redis:
  maxmemory: 850mb
  image: "docker.io/library/redis:7.4"
  resources:
    limits:
      memory: 1Gi
auth:
  enabled: true
  image: quay.io/keycloak/keycloak:26.0
  extra: ignored
unrelated:
  image: should-not-match
EOF
CHART_DIR="$chart_tmp"
expect_eq "redis image" "$(chart_image redis)" "docker.io/library/redis:7.4"
expect_eq "auth image" "$(chart_image auth)" "quay.io/keycloak/keycloak:26.0"
expect_eq "missing section" "$(chart_image postgres)" ""
# Nested image under redis.resources must not win over redis.image (already matched).
# A later top-level image must not leak into an earlier section.
expect_eq "does not leak later image" "$(chart_image redis)" "docker.io/library/redis:7.4"
rm -rf "$chart_tmp"

# ---------------------------------------------------------------------------
# Helm index record parser — must only accept the official releases URL + digest
# ---------------------------------------------------------------------------
helm_awk="$(awk '
  /chart_record=/ && /awk -v wanted/ { grab = 1; next }
  grab && /helm_index"\)"/ { exit }
  grab { print }
' "$BUNDLE")"
[[ -n "$helm_awk" ]] || fail "failed to extract helm-index awk from bundler"

parse_helm_chart_record() {
  local helm_index="$1"
  local chart_version="$2"
  awk -v wanted="$chart_version" "$helm_awk" "$helm_index"
}

index_tmp="$(mktemp)"
cat > "$index_tmp" <<'EOF'
apiVersion: v1
entries:
  odigos:
  - apiVersion: v2
    version: 1.36.0
    urls:
    - https://github.com/odigos-io/odigos/releases/download/v1.36.0/odigos-1.36.0.tgz
    digest: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  odigos-central:
  - apiVersion: v2
    digest: 1111111111111111111111111111111111111111111111111111111111111111
    urls:
    - https://github.com/odigos-io/odigos/releases/download/v1.35.0/odigos-central-1.35.0.tgz
    version: 1.35.0
  - apiVersion: v2
    digest: 2222222222222222222222222222222222222222222222222222222222222222
    urls:
    - https://github.com/odigos-io/odigos/releases/download/v1.36.0/odigos-central-1.36.0.tgz
    version: "1.36.0"
  odigos-ui:
  - apiVersion: v2
    digest: 3333333333333333333333333333333333333333333333333333333333333333
    urls:
    - https://evil.example/odigos-central-1.36.0.tgz
    version: 1.36.0
EOF
expect_eq "selects matching central chart" \
  "$(parse_helm_chart_record "$index_tmp" "1.36.0")" \
  "2222222222222222222222222222222222222222222222222222222222222222|https://github.com/odigos-io/odigos/releases/download/v1.36.0/odigos-central-1.36.0.tgz"
expect_eq "selects older central chart" \
  "$(parse_helm_chart_record "$index_tmp" "1.35.0")" \
  "1111111111111111111111111111111111111111111111111111111111111111|https://github.com/odigos-io/odigos/releases/download/v1.35.0/odigos-central-1.35.0.tgz"
expect_eq "does not take sibling chart" \
  "$(parse_helm_chart_record "$index_tmp" "9.9.9")" \
  ""
rm -f "$index_tmp"

# ---------------------------------------------------------------------------
# Bundle archive path regex — extraction refuses traversal / absolute entries
# ---------------------------------------------------------------------------
bundle_path_re="$(sed -n 's/.*\[\[ "\$bundle_entry" =~ \(.*\) \]\] || die "bundle contains an unsafe path:.*/\1/p' "$INSTALL")"
[[ -n "$bundle_path_re" ]] || fail "failed to extract bundle path regex"
grep -Fq '*/../* | */./* | *//*) die "bundle contains an unsafe path component' "$INSTALL" || \
  fail "installer is missing the path-component case that rejects .. and ."

# Mirrors the installer's two-step check: charset regex, then component case.
is_safe_bundle_path() {
  local bundle_entry="$1"
  [[ "$bundle_entry" =~ $bundle_path_re ]] || return 1
  case "/${bundle_entry}" in
    */../* | */./* | *//*) return 1 ;;
  esac
  return 0
}

expect_ok "bundle file" is_safe_bundle_path "bin/k3s"
expect_ok "bundle nested" is_safe_bundle_path "chart/templates/deployment.yaml"
expect_ok "bundle dir slash" is_safe_bundle_path "images/"
expect_fail "bundle mid-path dotdot" is_safe_bundle_path "bin/../etc/passwd"
expect_fail "bundle leading dotdot" is_safe_bundle_path "../etc/passwd"
expect_fail "bundle mid-path dot" is_safe_bundle_path "bin/./k3s"
expect_fail "bundle abs" is_safe_bundle_path "/etc/passwd"
expect_fail "bundle leading slash" is_safe_bundle_path "/bin/k3s"
expect_fail "bundle spaces" is_safe_bundle_path "bin/k3s file"

# ---------------------------------------------------------------------------
# sha256_file / file_size
# ---------------------------------------------------------------------------
hash_tmp="$(mktemp)"
printf 'hello' > "$hash_tmp"
expect_eq "sha256 hello" "$(sha256_file "$hash_tmp")" \
  "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
expect_eq "file size" "$(file_size "$hash_tmp")" "5"
rm -f "$hash_tmp"

# ---------------------------------------------------------------------------
# acquire_lock — concurrent bundle builds for the same version/arch
# ---------------------------------------------------------------------------
CENTRAL_VERSION="v1.36.0"
TARGET_ARCH="amd64"
acquired_lock_dirs=()
lock_root="$(mktemp -d)"
lock_dir="${lock_root}/v1.36.0-linux-amd64.lock"

log() { :; } # silence lock logs in tests

acquire_lock "$lock_dir" "output archive"
expect_eq "lock pid is this shell" "$(cat "${lock_dir}/pid")" "$$"

if ( acquire_lock "$lock_dir" "output archive" ) >/dev/null 2>&1; then
  fail "live lock must refuse a second acquirer"
else
  pass
fi

printf '1999999\n' > "${lock_dir}/pid"
acquire_lock "$lock_dir" "output archive"
expect_eq "stale lock reused" "$(cat "${lock_dir}/pid")" "$$"

rm -rf "$lock_root"

# ---------------------------------------------------------------------------
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
