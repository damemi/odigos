#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

BUNDLE_ARCHIVE=""
BUNDLE_DIR=""

HOST_NAME=""
TLS_CERT_FILE=""
TLS_KEY_FILE=""
TOKEN_FILE="${ODIGOS_ONPREM_TOKEN_FILE:-}"
NAMESPACE="odigos-central"
DATA_DIR="/var/lib/rancher/k3s"
NODE_IP=""
KUBECTL_USER="${SUDO_USER:-}"
KUBECTL_USER_UID=""
KUBECTL_USER_GID=""
KUBECTL_USER_HOME=""
USER_KUBECONFIG=""
USER_KUBECONFIG_IS_DEFAULT=false
SKIP_RESOURCE_CHECKS=false
MIN_CPUS=4
MIN_MEMORY_GIB=8
MIN_DISK_GIB=80
STATE_DIR="/etc/odigos-central"
MARKER_FILE="${STATE_DIR}/install.env"
WORK_DIR=""
INSTALL_LOG=""

usage() {
  cat <<'EOF'
Install a production-oriented, single-node Odigos Central cluster from an offline bundle.

Usage:
  sudo /opt/odigos/install-central-single-node.sh \
    --bundle /opt/odigos-central-v1.36.0-linux-amd64.tar.gz \
    --hostname central.example.com \
    --tls-cert /secure/fullchain.pem --tls-key /secure/privkey.pem \
    --token-file /secure/odigos-onprem-token

Required:
  --bundle ABS_PATH         Offline bundle .tar.gz produced by the bundler
  --hostname NAME           DNS name used to access Central
  --tls-cert ABS_PATH       PEM certificate/full chain valid for --hostname
  --tls-key ABS_PATH        PEM private key matching --tls-cert
  --token-file ABS_PATH     File containing the Odigos on-prem token

Options:
  --data-dir ABS_PATH       K3s data and persistent-volume root
                            (default: /var/lib/rancher/k3s)
  --namespace NAME          Central namespace (default: odigos-central)
  --node-ip ADDRESS         Explicit K3s node address for multi-interface hosts
  --kubectl-user NAME       User receiving a private kubeconfig
                            (default: the non-root sudo caller)
  --no-user-kubeconfig      Do not create a non-root kubeconfig
  --skip-resource-checks    Skip CPU, memory, and free-disk minimum checks
  -h, --help                Show this help

Host requirements:
  Linux with systemd, root access, a default network route, synchronized time,
  no swap, and free TCP ports 80, 443, 3000, 8081, 8080, and 6443. The default
  minimum is 4 CPUs, 8 GiB RAM, and 80 GiB free disk. On SELinux Enforcing
  hosts, install the distro-supported k3s-selinux package first.

  HTTPS is published to the network on 443. Native Central service ports are
  published on host loopback only: UI 3000, backend 8081, and Keycloak 8080.
  Redis and connector PostgreSQL remain cluster-internal.

  Every installation phase and command output is written to a timestamped file
  under /var/log/odigos-central. Secret values are never printed by the script.

The installer validates and extracts the bundle into a root-only temporary
workspace; callers must not extract it first. For compatibility, --bundle-dir
is accepted as an alias for --bundle.
The installer is independent from the bundle builder and may live anywhere.
The install is idempotent on a cluster previously created by this script. It
refuses to modify an unrelated existing K3s installation.
EOF
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

warn() {
  printf 'WARNING: %s\n' "$*" >&2
}

log() {
  printf '%s ==> %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

dump_k3s_diagnostics() {
  log "K3s service diagnostics"
  systemctl --no-pager --full status k3s.service 2>&1 || true
  if command -v journalctl >/dev/null 2>&1; then
    journalctl --no-pager --unit k3s.service --lines 100 2>&1 || true
  fi
}

wait_for_k3s_api() {
  local attempt
  local active_state
  local sub_state

  log "Waiting up to six minutes for K3s and the Kubernetes API"
  for attempt in $(seq 1 180); do
    if /usr/local/bin/k3s kubectl \
      --kubeconfig /etc/rancher/k3s/k3s.yaml \
      --request-timeout=10s \
      get --raw=/readyz >/dev/null 2>&1; then
      log "K3s and the Kubernetes API are ready"
      return 0
    fi

    active_state="$(systemctl show k3s.service --property=ActiveState --value 2>/dev/null || true)"
    sub_state="$(systemctl show k3s.service --property=SubState --value 2>/dev/null || true)"
    if [[ "$active_state" == "failed" ]] || \
       { (( attempt > 5 )) && [[ "$active_state" == "inactive" ]]; }; then
      dump_k3s_diagnostics
      die "K3s stopped while waiting for the Kubernetes API (state ${active_state:-unknown}/${sub_state:-unknown})"
    fi
    sleep 2
  done

  dump_k3s_diagnostics
  die "Kubernetes API did not become ready within six minutes"
}

cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    log "Cleaning temporary installation workspace: ${WORK_DIR}"
    rm -rf "$WORK_DIR"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

validate_dns_name() {
  local name="$1"
  [[ ${#name} -le 253 ]] || return 1
  [[ "$name" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || return 1
  [[ "$name" == *.* ]] || return 1
  [[ "$name" != *..* ]]
}

validate_path() {
  local normalized_path

  [[ "$1" =~ ^/[A-Za-z0-9._+/-]+$ ]] || return 1
  normalized_path="/${1#/}/"
  [[ "$normalized_path" != *"//"* && \
     "$normalized_path" != *"/../"* && \
     "$normalized_path" != *"/./"* ]]
}

normalize_arch() {
  case "$1" in
    x86_64 | amd64) printf 'amd64\n' ;;
    aarch64 | arm64) printf 'arm64\n' ;;
    *) die "unsupported server architecture: $1" ;;
  esac
}

configure_user_kubeconfig() {
  local user_kube_dir
  local default_kubeconfig

  [[ -n "$KUBECTL_USER" ]] || return
  user_kube_dir="${KUBECTL_USER_HOME}/.kube"
  USER_KUBECONFIG="${user_kube_dir}/odigos-central.yaml"
  default_kubeconfig="${user_kube_dir}/config"
  log "Configuring non-root kubectl access for ${KUBECTL_USER}"
  install -d -m 0700 -o "$KUBECTL_USER_UID" -g "$KUBECTL_USER_GID" "$user_kube_dir"
  install -m 0600 -o "$KUBECTL_USER_UID" -g "$KUBECTL_USER_GID" /etc/rancher/k3s/k3s.yaml "$USER_KUBECONFIG"

  if [[ -L "$default_kubeconfig" && "$(readlink "$default_kubeconfig")" == "odigos-central.yaml" ]]; then
    USER_KUBECONFIG_IS_DEFAULT=true
  elif [[ ! -e "$default_kubeconfig" && ! -L "$default_kubeconfig" ]]; then
    ln -s odigos-central.yaml "$default_kubeconfig"
    USER_KUBECONFIG_IS_DEFAULT=true
    log "Created default kubeconfig link for ${KUBECTL_USER}: ${default_kubeconfig}"
  else
    USER_KUBECONFIG_IS_DEFAULT=false
    log "Preserving existing kubeconfig: ${default_kubeconfig}"
    log "Use the dedicated Central kubeconfig with: export KUBECONFIG=${USER_KUBECONFIG}"
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bundle | --bundle-dir)
      [[ $# -ge 2 ]] || die "$1 requires a value"
      BUNDLE_ARCHIVE="$2"
      shift 2
      ;;
    --hostname)
      [[ $# -ge 2 ]] || die "--hostname requires a value"
      HOST_NAME="$2"
      shift 2
      ;;
    --tls-cert)
      [[ $# -ge 2 ]] || die "--tls-cert requires a value"
      TLS_CERT_FILE="$2"
      shift 2
      ;;
    --tls-key)
      [[ $# -ge 2 ]] || die "--tls-key requires a value"
      TLS_KEY_FILE="$2"
      shift 2
      ;;
    --token-file)
      [[ $# -ge 2 ]] || die "--token-file requires a value"
      TOKEN_FILE="$2"
      shift 2
      ;;
    --data-dir)
      [[ $# -ge 2 ]] || die "--data-dir requires a value"
      DATA_DIR="${2%/}"
      shift 2
      ;;
    --namespace)
      [[ $# -ge 2 ]] || die "--namespace requires a value"
      NAMESPACE="$2"
      shift 2
      ;;
    --node-ip)
      [[ $# -ge 2 ]] || die "--node-ip requires a value"
      NODE_IP="$2"
      shift 2
      ;;
    --kubectl-user)
      [[ $# -ge 2 ]] || die "--kubectl-user requires a value"
      KUBECTL_USER="$2"
      shift 2
      ;;
    --no-user-kubeconfig)
      KUBECTL_USER=""
      shift
      ;;
    --skip-resource-checks)
      SKIP_RESOURCE_CHECKS=true
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "$EUID" -eq 0 ]] || die "run this installer as root"

for logging_command in date mkdir chmod tee touch; do
  command -v "$logging_command" >/dev/null 2>&1 || die "required command not found: ${logging_command}"
done
mkdir -p /var/log/odigos-central
chmod 0700 /var/log/odigos-central
INSTALL_LOG="/var/log/odigos-central/install-$(date -u '+%Y%m%dT%H%M%SZ').log"
touch "$INSTALL_LOG"
chmod 0600 "$INSTALL_LOG"
exec > >(tee -a "$INSTALL_LOG") 2>&1
log "Starting Odigos Central single-node installation"
log "Installation transcript: ${INSTALL_LOG}"

[[ -n "$BUNDLE_ARCHIVE" ]] || die "--bundle is required"
[[ -n "$HOST_NAME" ]] || die "--hostname is required"
[[ -n "$TLS_CERT_FILE" ]] || die "--tls-cert is required"
[[ -n "$TLS_KEY_FILE" ]] || die "--tls-key is required"
[[ -n "$TOKEN_FILE" ]] || die "--token-file is required"
validate_dns_name "$HOST_NAME" || die "--hostname must be a valid fully-qualified DNS name"
validate_path "$BUNDLE_ARCHIVE" || die "--bundle must be an absolute path containing only letters, digits, '.', '_', '+', '-', and '/'"
validate_path "$DATA_DIR" || die "--data-dir must be an absolute path containing only letters, digits, '.', '_', '-', and '/'"
validate_path "$TLS_CERT_FILE" || die "--tls-cert must be an absolute path containing only letters, digits, '.', '_', '-', and '/'"
validate_path "$TLS_KEY_FILE" || die "--tls-key must be an absolute path containing only letters, digits, '.', '_', '-', and '/'"
validate_path "$TOKEN_FILE" || die "--token-file must be an absolute path containing only letters, digits, '.', '_', '-', and '/'"
[[ "$NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#NAMESPACE} -le 63 ]] || die "invalid namespace: $NAMESPACE"
if [[ "$KUBECTL_USER" == "root" ]]; then
  KUBECTL_USER=""
fi
if [[ -n "$KUBECTL_USER" ]]; then
  [[ "$KUBECTL_USER" =~ ^[A-Za-z_][A-Za-z0-9_.-]*[$]?$ ]] || die "invalid --kubectl-user value: ${KUBECTL_USER}"
fi
if [[ -n "$NODE_IP" ]]; then
  [[ "$NODE_IP" =~ ^[0-9A-Fa-f:.]+$ ]] || die "invalid --node-ip value"
fi

for path in "$TLS_CERT_FILE" "$TLS_KEY_FILE" "$TOKEN_FILE"; do
  [[ -f "$path" && -r "$path" ]] || die "required file is not readable: $path"
done
[[ -s "$TOKEN_FILE" ]] || die "the on-prem token file is empty"

require_command awk
require_command base64
require_command basename
require_command cmp
require_command cp
require_command df
require_command dirname
require_command find
require_command getconf
require_command grep
require_command ln
require_command ip
require_command install
require_command modprobe
require_command od
require_command openssl
require_command readlink
require_command sed
require_command seq
require_command sha256sum
require_command sleep
require_command sort
require_command ss
require_command systemctl
require_command sysctl
require_command tar
require_command timeout
require_command touch
require_command tr
require_command wc
if [[ -n "$KUBECTL_USER" ]]; then
  require_command getent
  kubectl_user_record="$(getent passwd "$KUBECTL_USER")"
  [[ -n "$kubectl_user_record" ]] || die "kubectl user does not exist: ${KUBECTL_USER}"
  IFS=: read -r _ _ KUBECTL_USER_UID KUBECTL_USER_GID _ KUBECTL_USER_HOME _ <<< "$kubectl_user_record"
  [[ "$KUBECTL_USER_UID" =~ ^[0-9]+$ && "$KUBECTL_USER_GID" =~ ^[0-9]+$ ]] || die "could not resolve uid/gid for kubectl user: ${KUBECTL_USER}"
  (( KUBECTL_USER_UID != 0 )) || die "--kubectl-user must identify a non-root user"
  validate_path "$KUBECTL_USER_HOME" || die "kubectl user has an unsupported home directory path: ${KUBECTL_USER_HOME}"
  [[ -d "$KUBECTL_USER_HOME" ]] || die "kubectl user home directory does not exist: ${KUBECTL_USER_HOME}"
fi

[[ -f "$BUNDLE_ARCHIVE" && -r "$BUNDLE_ARCHIVE" && ! -L "$BUNDLE_ARCHIVE" ]] || die "--bundle must identify a readable regular file, not a symbolic link: ${BUNDLE_ARCHIVE}"
tar --version 2>/dev/null | grep -q 'GNU tar' || die "GNU tar is required on the Linux installation host"

WORK_DIR="$(mktemp -d /tmp/odigos-central-install.XXXXXX)"
bundle_entries="${WORK_DIR}/bundle-entries.txt"
bundle_types="${WORK_DIR}/bundle-types.txt"
bundle_extract_root="${WORK_DIR}/extracted"
bundle_checksum_file="${BUNDLE_ARCHIVE}.sha256"
tar_bundle_args=(
  --warning=no-unknown-keyword
  --exclude='._*'
  --exclude='*/._*'
  --exclude='.DS_Store'
  --exclude='*/.DS_Store'
)

log "Reading offline bundle: ${BUNDLE_ARCHIVE}"
bundle_archive_digest="$(sha256sum "$BUNDLE_ARCHIVE" | awk '{print $1}')"
log "Bundle archive SHA-256: ${bundle_archive_digest}"
if [[ -f "$bundle_checksum_file" && -r "$bundle_checksum_file" ]]; then
  expected_bundle_digest="$(awk -v wanted="$(basename "$BUNDLE_ARCHIVE")" '
    {
      name = $2
      sub(/^\*/, "", name)
      if (name == wanted) { print $1; exit }
    }
  ' "$bundle_checksum_file")"
  [[ "$expected_bundle_digest" =~ ^[0-9a-fA-F]{64}$ ]] || die "bundle checksum file does not contain a valid entry for $(basename "$BUNDLE_ARCHIVE"): ${bundle_checksum_file}"
  [[ "$bundle_archive_digest" == "$expected_bundle_digest" ]] || die "bundle archive checksum mismatch (expected ${expected_bundle_digest}, got ${bundle_archive_digest})"
  log "Verified bundle archive against adjacent checksum file: ${bundle_checksum_file}"
else
  warn "adjacent bundle checksum file is unavailable: ${bundle_checksum_file}; internal payload checksums will still be verified"
fi

log "Inspecting bundle archive paths and file types before extraction"
tar "${tar_bundle_args[@]}" -tzf "$BUNDLE_ARCHIVE" > "$bundle_entries"
[[ -s "$bundle_entries" ]] || die "bundle archive is empty"
while IFS= read -r bundle_entry; do
  [[ "$bundle_entry" =~ ^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)*/?$ ]] || die "bundle contains an unsafe path: ${bundle_entry}"
  case "/${bundle_entry}" in
    */../* | */./* | *//*) die "bundle contains an unsafe path component: ${bundle_entry}" ;;
  esac
done < "$bundle_entries"

if ! tar "${tar_bundle_args[@]}" -tvzf "$BUNDLE_ARCHIVE" | awk '
  substr($1, 1, 1) != "-" && substr($1, 1, 1) != "d" { print; bad = 1 }
  END { exit bad }
' > "$bundle_types"; then
  sed -n '1,20p' "$bundle_types" >&2
  die "bundle contains symbolic links, hard links, devices, or another unsupported entry type"
fi

bundle_top_level_count="$(awk -F/ 'NF { print $1 }' "$bundle_entries" | LC_ALL=C sort -u | wc -l | tr -d '[:space:]')"
[[ "$bundle_top_level_count" == "1" ]] || die "bundle must contain exactly one top-level directory; found ${bundle_top_level_count}"
bundle_top_level="$(awk -F/ 'NF { print $1; exit }' "$bundle_entries")"
install -d -m 0700 "$bundle_extract_root"
log "Extracting the validated bundle into the temporary workspace"
log "  source: ${BUNDLE_ARCHIVE}"
log "  destination: ${bundle_extract_root}"
tar "${tar_bundle_args[@]}" --extract --gzip --file "$BUNDLE_ARCHIVE" --directory "$bundle_extract_root" --no-same-owner --no-same-permissions
post_extract_archive_digest="$(sha256sum "$BUNDLE_ARCHIVE" | awk '{print $1}')"
[[ "$post_extract_archive_digest" == "$bundle_archive_digest" ]] || die "bundle archive changed while it was being validated and extracted"
BUNDLE_DIR="${bundle_extract_root}/${bundle_top_level}"

[[ -f "${BUNDLE_DIR}/metadata/bundle.env" ]] || die "bundle metadata is missing after extraction: ${BUNDLE_DIR}"
[[ -f "${BUNDLE_DIR}/SHA256SUMS" ]] || die "bundle checksums are missing"
[[ -x "${BUNDLE_DIR}/bin/k3s" && -x "${BUNDLE_DIR}/bin/helm" && -x "${BUNDLE_DIR}/bin/k3s-install.sh" ]] || die "bundle executables are missing"
central_image_archives=()
while IFS= read -r central_image_archive; do
  central_image_archives+=("$central_image_archive")
done < <(find "${BUNDLE_DIR}/images" -maxdepth 1 -type f \( -name 'odigos-central-images.tar' -o -name 'odigos-central-image-*.oci.tar' \) | LC_ALL=C sort)
[[ ${#central_image_archives[@]} -gt 0 ]] || die "Central image archives are missing"

log "Verifying every extracted bundle artifact"
(cd "$BUNDLE_DIR" && sha256sum --check --strict SHA256SUMS)
expected_bundle_files="${WORK_DIR}/expected-bundle-files.txt"
actual_bundle_files="${WORK_DIR}/actual-bundle-files.txt"
awk '{ name = $2; sub(/^\*/, "", name); print name }' "${BUNDLE_DIR}/SHA256SUMS" | LC_ALL=C sort > "$expected_bundle_files"
while IFS= read -r bundle_file; do
  printf '%s\n' "${bundle_file#"${BUNDLE_DIR}/"}"
done < <(find "$BUNDLE_DIR" -type f ! -name SHA256SUMS | LC_ALL=C sort) > "$actual_bundle_files"
if ! cmp -s "$expected_bundle_files" "$actual_bundle_files"; then
  log "Expected payload file count: $(wc -l < "$expected_bundle_files" | tr -d '[:space:]')"
  log "Actual payload file count: $(wc -l < "$actual_bundle_files" | tr -d '[:space:]')"
  die "extracted bundle contains missing or unchecksummed payload files"
fi
log "Verified exact bundle file inventory"

read_bundle_value() {
  local key="$1"
  sed -n "s/^${key}='\([^']*\)'$/\1/p" "${BUNDLE_DIR}/metadata/bundle.env"
}

CENTRAL_VERSION="$(read_bundle_value CENTRAL_VERSION)"
TARGET_ARCH="$(read_bundle_value TARGET_ARCH)"
K3S_VERSION="$(read_bundle_value K3S_VERSION)"
HELM_VERSION="$(read_bundle_value HELM_VERSION)"
IMAGE_PREFIX="$(read_bundle_value IMAGE_PREFIX)"
: "${CENTRAL_VERSION:?bundle is missing CENTRAL_VERSION}"
: "${TARGET_ARCH:?bundle is missing TARGET_ARCH}"
: "${K3S_VERSION:?bundle is missing K3S_VERSION}"
: "${HELM_VERSION:?bundle is missing HELM_VERSION}"
: "${IMAGE_PREFIX:?bundle is missing IMAGE_PREFIX}"
[[ "$CENTRAL_VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || die "invalid Central version in bundle metadata"
[[ "$K3S_VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || die "invalid K3s version in bundle metadata"
[[ "$HELM_VERSION" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || die "invalid Helm version in bundle metadata"
[[ "$IMAGE_PREFIX" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._/-]+)?$ ]] || die "invalid image prefix in bundle metadata"

server_arch="$(normalize_arch "$(uname -m)")"
[[ "$server_arch" == "$TARGET_ARCH" ]] || die "bundle architecture is ${TARGET_ARCH}, server architecture is ${server_arch}"

log "Validating TLS certificate and key"
openssl x509 -in "$TLS_CERT_FILE" -noout >/dev/null
openssl pkey -in "$TLS_KEY_FILE" -noout >/dev/null
openssl x509 -in "$TLS_CERT_FILE" -checkend 604800 -noout >/dev/null || die "TLS certificate expires in less than seven days"
openssl x509 -in "$TLS_CERT_FILE" -checkhost "$HOST_NAME" -noout >/dev/null || die "TLS certificate is not valid for ${HOST_NAME}"
cert_key_hash="$(openssl x509 -in "$TLS_CERT_FILE" -pubkey -noout | openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
private_key_hash="$(openssl pkey -in "$TLS_KEY_FILE" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
[[ "$cert_key_hash" == "$private_key_hash" ]] || die "TLS certificate and private key do not match"

if command -v getenforce >/dev/null 2>&1 && [[ "$(getenforce)" == "Enforcing" ]]; then
  command -v rpm >/dev/null 2>&1 || die "SELinux is Enforcing but rpm is unavailable"
  rpm -q k3s-selinux >/dev/null 2>&1 || die "SELinux is Enforcing; install the supported k3s-selinux package first"
fi

if command -v swapon >/dev/null 2>&1 && [[ -n "$(swapon --show --noheadings 2>/dev/null)" ]]; then
  die "swap is enabled; disable it persistently before installing Kubernetes"
fi

if ! ip route show default 2>/dev/null | grep -q .; then
  die "K3s requires a default network route, including on an isolated host"
fi

managed_install=false
if [[ -f "$MARKER_FILE" ]]; then
  managed_install=true
elif systemctl list-unit-files --no-legend k3s.service 2>/dev/null | grep -q '^k3s\.service[[:space:]]' || [[ -e /etc/systemd/system/k3s.service ]]; then
  die "an existing K3s installation is present but was not created by this installer"
fi

if $managed_install; then
  existing_data_dir="$(sed -n "s/^DATA_DIR='\([^']*\)'$/\1/p" "$MARKER_FILE")"
  existing_namespace="$(sed -n "s/^NAMESPACE='\([^']*\)'$/\1/p" "$MARKER_FILE")"
  [[ -z "$existing_data_dir" || "$existing_data_dir" == "$DATA_DIR" ]] || die "an existing managed install uses data-dir ${existing_data_dir}; moving it is not supported"
  [[ -z "$existing_namespace" || "$existing_namespace" == "$NAMESPACE" ]] || die "an existing managed install uses namespace ${existing_namespace}; changing it is not supported"
fi

if ! $SKIP_RESOURCE_CHECKS; then
  cpu_count="$(getconf _NPROCESSORS_ONLN)"
  memory_kib="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
  memory_min_kib=$((MIN_MEMORY_GIB * 1024 * 1024))
  disk_probe="$DATA_DIR"
  while [[ ! -e "$disk_probe" && "$disk_probe" != "/" ]]; do
    disk_probe="$(dirname "$disk_probe")"
  done
  disk_available_kib="$(df -Pk "$disk_probe" | awk 'NR == 2 {print $4}')"
  disk_min_kib=$((MIN_DISK_GIB * 1024 * 1024))
  (( cpu_count >= MIN_CPUS )) || die "${MIN_CPUS} CPUs are required; found ${cpu_count}"
  (( memory_kib >= memory_min_kib )) || die "${MIN_MEMORY_GIB} GiB RAM is required"
  (( disk_available_kib >= disk_min_kib )) || die "${MIN_DISK_GIB} GiB free disk is required under ${disk_probe}"
fi

if command -v timedatectl >/dev/null 2>&1; then
  ntp_state="$(timedatectl show --property=NTPSynchronized --value 2>/dev/null || true)"
  [[ "$ntp_state" == "yes" ]] || warn "the host does not report synchronized time; configure a reliable local or upstream time source"
fi

if ! $managed_install; then
  for port in 80 443 3000 8081 8080 6443; do
    if ss -H -ltn | awk '{print $4}' | grep -Eq "(^|\]|:)${port}$"; then
      die "TCP port ${port} is already in use"
    fi
  done
fi

filesystem_probe="$DATA_DIR"
while [[ ! -e "$filesystem_probe" && "$filesystem_probe" != "/" ]]; do
  filesystem_probe="$(dirname "$filesystem_probe")"
done
if command -v stat >/dev/null 2>&1; then
  filesystem_type="$(stat -f -c %T "$filesystem_probe" 2>/dev/null || true)"
  case "$filesystem_type" in
    nfs | nfs4 | cifs | smb2) die "K3s data-dir must be on a local filesystem, not ${filesystem_type}" ;;
  esac
fi

install -d -m 0700 "$STATE_DIR" /etc/rancher/k3s "${DATA_DIR}/server/logs" "${DATA_DIR}/agent/images"
install -m 0600 "${BUNDLE_DIR}/metadata/images.txt" "${STATE_DIR}/images.txt"
cat > "$MARKER_FILE" <<EOF
STATUS='installing'
CENTRAL_VERSION='${CENTRAL_VERSION}'
K3S_VERSION='${K3S_VERSION}'
HELM_VERSION='${HELM_VERSION}'
IMAGE_PREFIX='${IMAGE_PREFIX}'
NAMESPACE='${NAMESPACE}'
HOST_NAME='${HOST_NAME}'
DATA_DIR='${DATA_DIR}'
KUBECTL_USER='${KUBECTL_USER}'
EOF
chmod 0600 "$MARKER_FILE"

log "Applying Kubernetes host kernel settings"
modprobe overlay
modprobe br_netfilter
cat > /etc/modules-load.d/k3s.conf <<'EOF'
overlay
br_netfilter
EOF
cat > /etc/sysctl.d/90-kubelet.conf <<'EOF'
vm.panic_on_oom=0
vm.overcommit_memory=1
kernel.panic=10
kernel.panic_on_oops=1
net.ipv4.ip_forward=1
net.bridge.bridge-nf-call-iptables=1
net.bridge.bridge-nf-call-ip6tables=1
EOF
sysctl -p /etc/sysctl.d/90-kubelet.conf >/dev/null

cat > "${DATA_DIR}/server/audit.yaml" <<'EOF'
apiVersion: audit.k8s.io/v1
kind: Policy
omitStages:
  - RequestReceived
rules:
  - level: Metadata
EOF
chmod 0600 "${DATA_DIR}/server/audit.yaml"

cat > "${DATA_DIR}/server/psa.yaml" <<'EOF'
apiVersion: apiserver.config.k8s.io/v1
kind: AdmissionConfiguration
plugins:
  - name: PodSecurity
    configuration:
      apiVersion: pod-security.admission.config.k8s.io/v1beta1
      kind: PodSecurityConfiguration
      defaults:
        enforce: baseline
        enforce-version: latest
        audit: restricted
        audit-version: latest
        warn: restricted
        warn-version: latest
      exemptions:
        usernames: []
        runtimeClasses: []
        namespaces:
          - kube-system
EOF
chmod 0600 "${DATA_DIR}/server/psa.yaml"

if [[ ! -s /etc/rancher/k3s/server-token ]]; then
  od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > /etc/rancher/k3s/server-token
fi
chmod 0600 /etc/rancher/k3s/server-token

cat > /etc/rancher/k3s/config.yaml <<EOF
data-dir: "${DATA_DIR}"
cluster-init: true
token-file: /etc/rancher/k3s/server-token
write-kubeconfig-mode: "0600"
secrets-encryption: true
protect-kernel-defaults: true
tls-san:
  - "${HOST_NAME}"
node-label:
  - "node-role.odigos.io/central=true"
etcd-snapshot-schedule-cron: "0 */6 * * *"
etcd-snapshot-retention: 28
etcd-snapshot-compress: true
kube-apiserver-arg:
  - "admission-control-config-file=${DATA_DIR}/server/psa.yaml"
  - "audit-log-path=${DATA_DIR}/server/logs/audit.log"
  - "audit-policy-file=${DATA_DIR}/server/audit.yaml"
  - "audit-log-maxage=30"
  - "audit-log-maxbackup=10"
  - "audit-log-maxsize=100"
  - "service-account-extend-token-expiration=false"
kube-controller-manager-arg:
  - "terminated-pod-gc-threshold=100"
kubelet-arg:
  - "streaming-connection-idle-timeout=5m"
  - "pod-max-pids=4096"
  - "system-reserved=cpu=500m,memory=1Gi,ephemeral-storage=2Gi"
  - "kube-reserved=cpu=500m,memory=1Gi,ephemeral-storage=2Gi"
  - "eviction-hard=memory.available<500Mi,nodefs.available<10%,imagefs.available<10%"
  - "tls-cipher-suites=TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305"
EOF
if [[ -n "$NODE_IP" ]]; then
  printf 'node-ip: "%s"\n' "$NODE_IP" >> /etc/rancher/k3s/config.yaml
fi
chmod 0600 /etc/rancher/k3s/config.yaml

k3s_airgap_archive="$(find "${BUNDLE_DIR}/images" -maxdepth 1 -type f -name "k3s-airgap-images-${TARGET_ARCH}.tar.zst" -print -quit)"
[[ -n "$k3s_airgap_archive" ]] || die "K3s air-gap image archive is missing"

if $managed_install && systemctl is-active --quiet k3s; then
  log "Taking a pre-change embedded-etcd snapshot"
  timeout --foreground 5m /usr/local/bin/k3s etcd-snapshot save \
    --name "pre-change-$(date -u +%Y%m%dT%H%M%SZ)"
fi

log "Staging K3s and Central air-gap images"
k3s_airgap_destination="${DATA_DIR}/agent/images/$(basename "$k3s_airgap_archive")"
install -m 0644 "$k3s_airgap_archive" "${k3s_airgap_destination}.odigos-new"
mv -f "${k3s_airgap_destination}.odigos-new" "$k3s_airgap_destination"
for central_image_archive in "${central_image_archives[@]}"; do
  log "Staging Central image archive: $(basename "$central_image_archive")"
  central_image_destination="${DATA_DIR}/agent/images/$(basename "$central_image_archive")"
  install -m 0644 "$central_image_archive" "${central_image_destination}.odigos-new"
  mv -f "${central_image_destination}.odigos-new" "$central_image_destination"
done
touch "${DATA_DIR}/agent/images/.cache.json"

log "Installing the K3s binary atomically"
install -m 0755 "${BUNDLE_DIR}/bin/k3s" /usr/local/bin/.k3s.odigos-new
mv -f /usr/local/bin/.k3s.odigos-new /usr/local/bin/k3s

log "Installing K3s ${K3S_VERSION} without network access"
if ! INSTALL_K3S_SKIP_DOWNLOAD=true \
  INSTALL_K3S_SKIP_SELINUX_RPM=true \
  INSTALL_K3S_SKIP_START=true \
  INSTALL_K3S_BIN_DIR=/usr/local/bin \
  INSTALL_K3S_EXEC=server \
    "${BUNDLE_DIR}/bin/k3s-install.sh"; then
  dump_k3s_diagnostics
  die "K3s service installation failed"
fi

log "Restarting K3s without blocking on systemd's unlimited start timeout"
systemctl reset-failed k3s.service >/dev/null 2>&1 || true
systemctl restart --no-block k3s.service || {
  dump_k3s_diagnostics
  die "could not queue the K3s restart"
}

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
KUBECTL=(/usr/local/bin/k3s kubectl --request-timeout=30s)
HELM="${BUNDLE_DIR}/bin/helm"

wait_for_k3s_api

if [[ -n "$KUBECTL_USER" ]]; then
  configure_user_kubeconfig
else
  log "Non-root kubeconfig creation is disabled; the root-only K3s kubeconfig remains at /etc/rancher/k3s/k3s.yaml"
fi

for deployment in coredns local-path-provisioner metrics-server traefik; do
  deployment_found=false
  for _ in $(seq 1 180); do
    if "${KUBECTL[@]}" --namespace kube-system get deployment "$deployment" >/dev/null 2>&1; then
      deployment_found=true
      break
    fi
    sleep 2
  done
  $deployment_found || die "required K3s deployment ${deployment} was not created"
  "${KUBECTL[@]}" --namespace kube-system rollout status "deployment/${deployment}" --timeout=10m
done

log "Creating the Central namespace and encrypted secrets"
"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" label namespace "$NAMESPACE" \
  odigos.io/system-object=true \
  pod-security.kubernetes.io/enforce=baseline \
  pod-security.kubernetes.io/enforce-version=latest \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/audit-version=latest \
  pod-security.kubernetes.io/warn=baseline \
  pod-security.kubernetes.io/warn-version=latest \
  --overwrite
log "Central namespace enforces Pod Security baseline; restricted deviations remain in the Kubernetes audit log"

normalized_token="${WORK_DIR}/ODIGOS_ONPREM_TOKEN"
tr -d '\r\n' < "$TOKEN_FILE" > "$normalized_token"
[[ -s "$normalized_token" ]] || die "the normalized on-prem token is empty"
"${KUBECTL[@]}" --namespace "$NAMESPACE" create secret generic odigos-central \
  --from-file="ODIGOS_ONPREM_TOKEN=${normalized_token}" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" --namespace "$NAMESPACE" label secret odigos-central \
  app.kubernetes.io/managed-by=Helm --overwrite
"${KUBECTL[@]}" --namespace "$NAMESPACE" annotate secret odigos-central \
  meta.helm.sh/release-name=odigos-central \
  meta.helm.sh/release-namespace="$NAMESPACE" \
  --overwrite
"${KUBECTL[@]}" --namespace "$NAMESPACE" create secret tls central-tls \
  --cert="$TLS_CERT_FILE" --key="$TLS_KEY_FILE" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

if "${KUBECTL[@]}" --namespace "$NAMESPACE" get secret odigos-connector-postgres >/dev/null 2>&1; then
  postgres_password="$("${KUBECTL[@]}" --namespace "$NAMESPACE" get secret odigos-connector-postgres -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 --decode)"
else
  postgres_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
fi
[[ "$postgres_password" =~ ^[A-Fa-f0-9]{64}$ ]] || die "connector PostgreSQL password has an unexpected format"

values_file="${STATE_DIR}/values.yaml"
cat > "$values_file" <<EOF
image:
  tag: "${CENTRAL_VERSION}"
imagePrefix: "${IMAGE_PREFIX}"
externalOnpremTokenSecret: true

centralBackend:
  maxMessageSize: "67108864"
  resources:
    requests:
      cpu: 250m
      memory: 512Mi
    limits:
      cpu: "2"
      memory: 2Gi
  autoscaling:
    enabled: true
    minReplicas: 1
    maxReplicas: 4
    cpuTargetUtilization: 70

centralUI:
  resources:
    requests:
      cpu: 250m
      memory: 512Mi
    limits:
      cpu: "1"
      memory: 1Gi

redis:
  maxmemory: 850mb
  resources:
    requests:
      cpu: 200m
      memory: 512Mi
    limits:
      cpu: "1"
      memory: 1Gi

auth:
  enabled: true
  externalUrl: "https://${HOST_NAME}"
  externalUIUrl: "https://${HOST_NAME}"
  persistence:
    enabled: true
    size: 10Gi
    accessModes:
      - ReadWriteOnce
    storageClassName: local-path
  resources:
    requests:
      cpu: 250m
      memory: 512Mi
    limits:
      cpu: "1"
      memory: 1Gi
  javaOpts: "-Xms256m -Xmx512m"

cloudConnectors:
  enabled: true
  connectorRuntime:
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi
  postgres:
    database: odigos_central
    username: odigos
    password: "${postgres_password}"
    persistence:
      enabled: true
      size: 50Gi
      accessModes:
        - ReadWriteOnce
      storageClassName: local-path
    resources:
      requests:
        cpu: 250m
        memory: 512Mi
      limits:
        cpu: "2"
        memory: 2Gi
EOF
chmod 0600 "$values_file"

"${KUBECTL[@]}" --namespace "$NAMESPACE" patch serviceaccount default --type=merge \
  --patch '{"automountServiceAccountToken":false}' >/dev/null

log "Installing the unmodified odigos-central chart"
timeout --foreground 25m "$HELM" upgrade --install odigos-central "${BUNDLE_DIR}/chart" \
  --namespace "$NAMESPACE" \
  --values "$values_file" \
  --rollback-on-failure \
  --wait \
  --wait-for-jobs \
  --timeout 20m \
  --history-max 10

log "Configuring TLS ingress and namespace network isolation"
for _ in $(seq 1 120); do
  if "${KUBECTL[@]}" get customresourcedefinition middlewares.traefik.io >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
"${KUBECTL[@]}" get customresourcedefinition middlewares.traefik.io >/dev/null 2>&1 || die "Traefik Middleware CRD is unavailable"

cat > "${WORK_DIR}/central-edge.yaml" <<EOF
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: central-redirect-https
  namespace: ${NAMESPACE}
spec:
  redirectScheme:
    scheme: https
    permanent: true
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: central-security-headers
  namespace: ${NAMESPACE}
spec:
  headers:
    contentTypeNosniff: true
    customFrameOptionsValue: SAMEORIGIN
    referrerPolicy: strict-origin-when-cross-origin
    stsSeconds: 31536000
    stsIncludeSubdomains: true
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: central-edge
  namespace: ${NAMESPACE}
spec:
  chain:
    middlewares:
      - name: central-redirect-https
      - name: central-security-headers
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: odigos-central
  namespace: ${NAMESPACE}
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: web,websecure
    traefik.ingress.kubernetes.io/router.middlewares: ${NAMESPACE}-central-edge@kubernetescrd
spec:
  ingressClassName: traefik
  tls:
    - hosts:
        - ${HOST_NAME}
      secretName: central-tls
  rules:
    - host: ${HOST_NAME}
      http:
        paths:
          - path: /ws
            pathType: Prefix
            backend:
              service:
                name: central-backend
                port:
                  number: 8081
          - path: /graphql
            pathType: Prefix
            backend:
              service:
                name: central-backend
                port:
                  number: 8081
          - path: /sse
            pathType: Prefix
            backend:
              service:
                name: central-backend
                port:
                  number: 8081
          - path: /realms
            pathType: Prefix
            backend:
              service:
                name: central-backend
                port:
                  number: 8081
          - path: /resources
            pathType: Prefix
            backend:
              service:
                name: central-backend
                port:
                  number: 8081
          - path: /
            pathType: Prefix
            backend:
              service:
                name: central-ui
                port:
                  number: 3000
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-ingress
  namespace: ${NAMESPACE}
spec:
  podSelector: {}
  policyTypes:
    - Ingress
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-central-internal
  namespace: ${NAMESPACE}
spec:
  podSelector: {}
  ingress:
    - from:
        - podSelector: {}
  policyTypes:
    - Ingress
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-traefik-to-central
  namespace: ${NAMESPACE}
spec:
  podSelector:
    matchExpressions:
      - key: app
        operator: In
        values:
          - central-ui
          - central-backend
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system
          podSelector:
            matchLabels:
              app.kubernetes.io/name: traefik
      ports:
        - protocol: TCP
          port: 3000
        - protocol: TCP
          port: 8081
  policyTypes:
    - Ingress
EOF
"${KUBECTL[@]}" apply -f "${WORK_DIR}/central-edge.yaml"

for deployment in central-backend central-ui redis keycloak connector-runtime odigos-connector-postgres; do
  "${KUBECTL[@]}" --namespace "$NAMESPACE" rollout status "deployment/${deployment}" --timeout=10m
done

expected_tls_fingerprint="$(openssl x509 -in "$TLS_CERT_FILE" -noout -sha256 -fingerprint)"
edge_ready=false
for _ in $(seq 1 120); do
  actual_tls_fingerprint="$(timeout 5 openssl s_client -connect 127.0.0.1:443 -servername "$HOST_NAME" < /dev/null 2>/dev/null | openssl x509 -noout -sha256 -fingerprint 2>/dev/null || true)"
  if [[ "$actual_tls_fingerprint" == "$expected_tls_fingerprint" ]]; then
    edge_ready=true
    break
  fi
  sleep 2
done
$edge_ready || die "TLS ingress did not serve the supplied certificate within four minutes"

log "Publishing Central service ports on host loopback"
port_forward_units=()
port_forward_mappings=(
  "ui|central-ui|3000"
  "backend|central-backend|8081"
  "keycloak|keycloak|8080"
)
for mapping in "${port_forward_mappings[@]}"; do
  IFS='|' read -r unit_suffix service_name service_port <<< "$mapping"
  unit_name="odigos-central-${unit_suffix}-port.service"
  unit_file="/etc/systemd/system/${unit_name}"
  cat > "$unit_file" <<EOF
[Unit]
Description=Odigos Central ${service_name} host port
Requires=k3s.service
After=k3s.service network-online.target

[Service]
Type=simple
Environment=HOME=/tmp
ExecStart=/usr/local/bin/k3s kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml --namespace ${NAMESPACE} port-forward --address=127.0.0.1 service/${service_name} ${service_port}:${service_port}
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$unit_file"
  port_forward_units+=("$unit_name")
done
systemctl daemon-reload
systemctl enable --now "${port_forward_units[@]}"

for mapping in "${port_forward_mappings[@]}"; do
  IFS='|' read -r _ _ service_port <<< "$mapping"
  port_ready=false
  for _ in $(seq 1 30); do
    if ss -H -ltn | awk '{print $4}' | grep -Eq "127\\.0\\.0\\.1:${service_port}$"; then
      port_ready=true
      break
    fi
    sleep 1
  done
  $port_ready || die "host loopback port ${service_port} did not become ready"
done

cat > "$MARKER_FILE" <<EOF
STATUS='ready'
CENTRAL_VERSION='${CENTRAL_VERSION}'
K3S_VERSION='${K3S_VERSION}'
HELM_VERSION='${HELM_VERSION}'
IMAGE_PREFIX='${IMAGE_PREFIX}'
NAMESPACE='${NAMESPACE}'
HOST_NAME='${HOST_NAME}'
DATA_DIR='${DATA_DIR}'
KUBECTL_USER='${KUBECTL_USER}'
EOF
chmod 0600 "$MARKER_FILE"

log "Installation complete"
"${KUBECTL[@]}" --namespace "$NAMESPACE" get pods,persistentvolumeclaims,ingress
printf '\nCentral URL: https://%s\n' "$HOST_NAME"
printf 'Host ports (loopback): UI 3000, backend 8081, Keycloak 8080\n'
printf 'Redis and connector PostgreSQL are cluster-internal only.\n'
printf 'Kubeconfig: /etc/rancher/k3s/k3s.yaml (root-only)\n'
if [[ -n "$USER_KUBECONFIG" ]]; then
  printf 'Non-root kubeconfig for %s: %s (mode 0600)\n' "$KUBECTL_USER" "$USER_KUBECONFIG"
  if $USER_KUBECONFIG_IS_DEFAULT; then
    printf 'Run as %s without sudo: kubectl get nodes\n' "$KUBECTL_USER"
  else
    printf 'Run as %s: export KUBECONFIG=%s\n' "$KUBECTL_USER" "$USER_KUBECONFIG"
  fi
fi
printf 'Local etcd snapshots: %s/server/db/snapshots\n' "$DATA_DIR"
printf 'Back up the etcd snapshots and %s/storage to independent storage.\n' "$DATA_DIR"
printf 'Restrict TCP 6443 at the host firewall to trusted administrator networks.\n'
printf 'A single node has no node-level high availability; restore testing is required.\n'
