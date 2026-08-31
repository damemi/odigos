#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# Prevent macOS bsdtar from serializing AppleDouble files and com.apple.*
# extended attributes. The variable is harmless on Linux and keeps the bundle
# byte-for-byte portable to GNU tar on the installation host.
export COPYFILE_DISABLE=1

OUTPUT_DIR="/var/tmp/odigos-central-bundles"
CACHE_DIR="/var/tmp/odigos-central-bundle-cache"
CHART_DIR=""
IMAGE_PREFIX="${ODIGOS_IMAGE_PREFIX:-registry.odigos.io}"
K3S_VERSION="${K3S_VERSION:-}"
K3S_CHANNEL="${K3S_CHANNEL:-stable}"
HELM_VERSION="${HELM_VERSION:-v4.2.3}"
HELM_REPO_INDEX_URL="https://odigos-io.github.io/odigos/index.yaml"
HELM_RELEASE_URL_PREFIX="https://github.com/odigos-io/odigos/releases/download/"
AWS_FARGATE_AGENTS_IMAGE="registry.odigos.io/odigos-fargate-agents:v0.0.15"
SERVERLESS_AGENTS_IMAGE="us-central1-docker.pkg.dev/odigos-cloud/components/cloud-run-agents:v0.1.0"
TARGET_ARCH=""
ALLOW_DIRTY_CHART=false
CENTRAL_VERSION=""
BUILD_LOG=""
build_dir=""
acquired_lock_dirs=()

usage() {
  cat <<'EOF'
Build a self-contained Odigos Central installation bundle.

Usage:
  bundle-central-offline.sh VERSION [options]

Arguments:
  VERSION                   Central image tag, for example v1.36.0

Options:
  --arch amd64|arm64        Target server architecture (default: host architecture)
  --chart-dir ABS_PATH      Explicit local chart override for development only
  --image-prefix PREFIX     Enterprise image registry/prefix (default: registry.odigos.io)
  --k3s-version VERSION     K3s release (default: resolve the stable K3s channel)
  --helm-version VERSION    Helm release (default: v4.2.3)
  --output-dir ABS_PATH     Output directory (default: /var/tmp/odigos-central-bundles)
  --cache-dir ABS_PATH      Download cache (default: /var/tmp/odigos-central-bundle-cache)
  --allow-dirty-chart       Permit a dirty explicit --chart-dir override
  -h, --help                Show this help

Environment:
  ODIGOS_ONPREM_TOKEN_FILE  Optional token file used for a temporary registry.odigos.io login
  DOCKER_CONFIG             Existing Docker credentials are honored

The builder downloads the released odigos-central chart for VERSION from the
official Odigos Helm repository. It has no dependency on an Odigos source-code
checkout or the current working directory. The independent installer,
uninstaller, and uninstallation guide are not inputs to, or embedded in, the
bundle.

The builder requires curl, Docker, tar, and sha256sum (or shasum). It downloads
the chart, K3s, and Helm; pulls the Central images; and writes a .tar.gz plus a checksum.
Every operation is printed and saved in a timestamped log under the output
directory. Downloads are cached and resumable; concurrent builds for the same
Central version and architecture are rejected to protect the cache and output.
EOF
}

die() {
  local message="ERROR: $*"
  printf '%s\n' "$message" >&2
  if [[ -n "$BUILD_LOG" ]]; then
    printf '%s\n' "$message" >> "$BUILD_LOG"
  fi
  exit 1
}

log() {
  local message
  message="$(date -u '+%Y-%m-%dT%H:%M:%SZ') ==> $*"
  printf '%s\n' "$message"
  if [[ -n "$BUILD_LOG" ]]; then
    printf '%s\n' "$message" >> "$BUILD_LOG"
  fi
}

file_size() {
  wc -c < "$1" | tr -d '[:space:]'
}

download_file() {
  local description="$1"
  local url="$2"
  local destination="$3"
  local partial="${destination}.part"

  mkdir -p "$(dirname "$destination")"
  log "Download: ${description}"
  log "  source: ${url}"
  log "  cache:  ${destination}"

  if [[ -f "$destination" ]]; then
    log "  result: using cached file ($(file_size "$destination") bytes, sha256 $(sha256_file "$destination"))"
    return
  fi

  if [[ -f "$partial" ]]; then
    log "  result: resuming partial download at $(file_size "$partial") bytes"
  else
    log "  result: starting new download"
  fi

  curl \
    --fail \
    --location \
    --retry 3 \
    --retry-all-errors \
    --continue-at - \
    --output "$partial" \
    "$url" 2>&1 | tee -a "$BUILD_LOG"
  [[ -f "$partial" ]] || die "download completed but its output file is missing: ${partial}"
  mv "$partial" "$destination"
  log "  result: completed ($(file_size "$destination") bytes, sha256 $(sha256_file "$destination"))"
}

verify_cached_download() {
  local description="$1"
  local url="$2"
  local path="$3"
  local expected="$4"
  local actual
  local quarantine

  actual="$(sha256_file "$path")"
  if [[ "$actual" == "$expected" ]]; then
    log "  verified: $(basename "$path") (${actual})"
    return
  fi

  quarantine="${path}.invalid-$(date -u '+%Y%m%dT%H%M%SZ')"
  log "  checksum mismatch: $(basename "$path")"
  log "    expected: ${expected}"
  log "    actual:   ${actual}"
  log "  quarantining invalid cache file: ${quarantine}"
  mv "$path" "$quarantine"
  if [[ -f "${path}.part" ]]; then
    mv "${path}.part" "${quarantine}.part"
  fi

  log "  downloading a clean replacement"
  download_file "$description" "$url" "$path"
  actual="$(sha256_file "$path")"
  [[ "$actual" == "$expected" ]] || die "checksum mismatch remains after re-downloading $(basename "$path") (expected ${expected}, got ${actual})"
  log "  verified replacement: $(basename "$path") (${actual})"
}

cleanup() {
  local exit_code=$?
  local index
  local current_lock
  trap - EXIT INT TERM
  if [[ -n "$build_dir" && -d "$build_dir" ]]; then
    log "Cleaning temporary build directory: ${build_dir}"
    rm -rf "$build_dir"
  fi
  for ((index = ${#acquired_lock_dirs[@]} - 1; index >= 0; index--)); do
    current_lock="${acquired_lock_dirs[$index]}"
    if [[ -d "$current_lock" ]]; then
      rm -f "${current_lock}/pid"
      rmdir "$current_lock"
      log "Released bundle lock: ${current_lock}"
    fi
  done
  exit "$exit_code"
}

acquire_lock() {
  local candidate="$1"
  local purpose="$2"
  local lock_pid

  if ! mkdir "$candidate" 2>/dev/null; then
    lock_pid="$(sed -n '1p' "${candidate}/pid" 2>/dev/null || true)"
    if [[ "$lock_pid" =~ ^[0-9]+$ ]] && kill -0 "$lock_pid" 2>/dev/null; then
      die "another bundle build is using the ${purpose} for ${CENTRAL_VERSION} linux/${TARGET_ARCH} (PID ${lock_pid}); lock: ${candidate}"
    fi
    log "Removing stale ${purpose} lock: ${candidate}"
    rm -f "${candidate}/pid"
    rmdir "$candidate" 2>/dev/null || die "${purpose} lock exists and is not an empty builder lock: ${candidate}"
    mkdir "$candidate" || die "could not acquire ${purpose} lock: ${candidate}"
  fi
  printf '%s\n' "$$" > "${candidate}/pid"
  acquired_lock_dirs+=("$candidate")
  log "Acquired exclusive ${purpose} lock: ${candidate}"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

normalize_arch() {
  case "$1" in
    amd64 | x86_64) printf 'amd64\n' ;;
    arm64 | aarch64) printf 'arm64\n' ;;
    *) die "unsupported architecture: $1 (expected amd64 or arm64)" ;;
  esac
}

validate_version() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || die "invalid $2: $1"
}

chart_image() {
  local section="$1"
  awk -v wanted="$section" '
    $0 == wanted ":" { in_section = 1; next }
    in_section && /^[^ ]/ { exit }
    in_section && /^  image:[[:space:]]*/ {
      sub(/^  image:[[:space:]]*/, "")
      gsub(/^["\047]|["\047]$/, "")
      print
      exit
    }
  ' "${CHART_DIR}/values.yaml"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --arch)
      [[ $# -ge 2 ]] || die "--arch requires a value"
      TARGET_ARCH="$(normalize_arch "$2")"
      shift 2
      ;;
    --chart-dir)
      [[ $# -ge 2 ]] || die "--chart-dir requires a value"
      CHART_DIR="$2"
      shift 2
      ;;
    --cache-dir)
      [[ $# -ge 2 ]] || die "--cache-dir requires a value"
      CACHE_DIR="${2%/}"
      shift 2
      ;;
    --image-prefix)
      [[ $# -ge 2 ]] || die "--image-prefix requires a value"
      IMAGE_PREFIX="${2%/}"
      shift 2
      ;;
    --k3s-version)
      [[ $# -ge 2 ]] || die "--k3s-version requires a value"
      K3S_VERSION="$2"
      shift 2
      ;;
    --helm-version)
      [[ $# -ge 2 ]] || die "--helm-version requires a value"
      HELM_VERSION="$2"
      shift 2
      ;;
    --output-dir)
      [[ $# -ge 2 ]] || die "--output-dir requires a value"
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --allow-dirty-chart)
      ALLOW_DIRTY_CHART=true
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    --*) die "unknown option: $1" ;;
    *)
      [[ -z "$CENTRAL_VERSION" ]] || die "unexpected argument: $1"
      CENTRAL_VERSION="$1"
      shift
      ;;
  esac
done

[[ -n "$CENTRAL_VERSION" ]] || {
  usage >&2
  exit 2
}

validate_version "$CENTRAL_VERSION" "Central version"
validate_version "$HELM_VERSION" "Helm version"
[[ "$IMAGE_PREFIX" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._/-]+)?$ ]] || die "invalid image prefix: $IMAGE_PREFIX"
chart_version="${CENTRAL_VERSION#v}"

for absolute_path_record in \
  "output directory|${OUTPUT_DIR}" \
  "cache directory|${CACHE_DIR}"; do
  path_label="${absolute_path_record%%|*}"
  path_value="${absolute_path_record#*|}"
  [[ "$path_value" == /* ]] || die "${path_label} must be an absolute path: ${path_value}"
done
if [[ -n "$CHART_DIR" ]]; then
  [[ "$CHART_DIR" == /* ]] || die "--chart-dir must be an absolute path"
else
  $ALLOW_DIRTY_CHART && die "--allow-dirty-chart is valid only with --chart-dir"
fi

if [[ -z "$TARGET_ARCH" ]]; then
  TARGET_ARCH="$(normalize_arch "$(uname -m)")"
fi

require_command awk
require_command curl
require_command docker
require_command find
require_command grep
require_command sed
require_command sort
require_command tar
require_command tee
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "sha256sum or shasum is required"
fi

mkdir -p "$OUTPUT_DIR"
mkdir -p "$CACHE_DIR/locks"
mkdir -p "$OUTPUT_DIR/.locks"
log_timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
BUILD_LOG="${OUTPUT_DIR}/bundle-${CENTRAL_VERSION}-linux-${TARGET_ARCH}-${log_timestamp}-$$.log"
: > "$BUILD_LOG"
log "Starting Odigos Central offline bundle"
log "Build log: ${BUILD_LOG}"
log "Requested Central version: ${CENTRAL_VERSION}"
log "Target platform: linux/${TARGET_ARCH}"
log "Portable archive mode: macOS metadata and extended attributes disabled"
log "Image prefix: ${IMAGE_PREFIX}"
if [[ -n "$CHART_DIR" ]]; then
  log "Chart source: explicit local override ${CHART_DIR}"
else
  log "Chart source: released odigos-central ${chart_version} from ${HELM_REPO_INDEX_URL}"
fi
log "Output directory: ${OUTPUT_DIR}"
log "Cache directory: ${CACHE_DIR}"

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
acquire_lock "${OUTPUT_DIR}/.locks/${CENTRAL_VERSION}-linux-${TARGET_ARCH}.lock" "output archive"
acquire_lock "${CACHE_DIR}/locks/${CENTRAL_VERSION}-linux-${TARGET_ARCH}.lock" "download cache"

if [[ -z "$K3S_VERSION" ]]; then
  log "Resolving K3s channel ${K3S_CHANNEL}"
  log "  source: https://update.k3s.io/v1-release/channels/${K3S_CHANNEL}"
  resolved_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://update.k3s.io/v1-release/channels/${K3S_CHANNEL}")"
  K3S_VERSION="$(basename "$resolved_url" | sed 's/%2[Bb]/+/g')"
  log "  result: ${K3S_VERSION}"
fi
validate_version "$K3S_VERSION" "K3s version"

log "Checking that the Docker daemon is available"
docker info >/dev/null 2>&1 || die "Docker is installed but its daemon is not available"
log "Docker daemon is available"

build_dir="$(mktemp -d "${TMPDIR:-/tmp}/odigos-central-bundle.XXXXXX")"

bundle_name="odigos-central-${CENTRAL_VERSION}-linux-${TARGET_ARCH}"
bundle_root="${build_dir}/${bundle_name}"
cache_root="$CACHE_DIR"
log "Temporary build directory: ${build_dir}"
log "Persistent download cache: ${cache_root}"
log "Creating bundle directory layout"
mkdir -p "${bundle_root}/bin" "${bundle_root}/chart" "${bundle_root}/images" "${bundle_root}/metadata"

chart_revision="released"
chart_dirty="false"
chart_source=""
chart_digest=""
if [[ -n "$CHART_DIR" ]]; then
  [[ -f "${CHART_DIR}/Chart.yaml" && -f "${CHART_DIR}/values.yaml" ]] || die "odigos-central chart not found at ${CHART_DIR}"
  chart_source="local:${CHART_DIR}"
  chart_revision="unknown"
  chart_dirty="unknown"
  if command -v git >/dev/null 2>&1; then
    chart_repo="$(git -C "$CHART_DIR" rev-parse --show-toplevel 2>/dev/null || true)"
    if [[ -n "$chart_repo" ]]; then
      chart_revision="$(git -C "$chart_repo" rev-parse HEAD)"
      chart_relative="${CHART_DIR#"${chart_repo}/"}"
      if [[ -n "$(git -C "$chart_repo" status --porcelain -- "$chart_relative")" ]]; then
        chart_dirty="true"
        $ALLOW_DIRTY_CHART || die "the local Central chart has uncommitted changes; commit them or use --allow-dirty-chart"
      else
        chart_dirty="false"
      fi
    fi
  fi
  log "Copying explicit local Central chart"
  log "  source: ${CHART_DIR}"
  log "  destination: ${bundle_root}/chart"
  (cd "$CHART_DIR" && tar -cf - .) | (cd "${bundle_root}/chart" && tar -xf -)
else
  helm_index="${build_dir}/odigos-helm-index.yaml"
  log "Downloading current official Odigos Helm repository index"
  log "  source: ${HELM_REPO_INDEX_URL}"
  curl --fail --location --retry 3 --retry-all-errors --output "$helm_index" "$HELM_REPO_INDEX_URL" 2>&1 | tee -a "$BUILD_LOG"
  log "  result: completed ($(file_size "$helm_index") bytes, sha256 $(sha256_file "$helm_index"))"
  chart_record="$(awk -v wanted="$chart_version" '
    $0 == "  odigos-central:" { in_chart = 1; next }
    in_chart && /^  [A-Za-z0-9_-]+:/ { exit }
    in_chart && /^  - apiVersion:/ { digest = ""; url = "" }
    in_chart && /^    digest:[[:space:]]*/ { digest = $2 }
    in_chart && /^    - https:\/\// { url = $2 }
    in_chart && /^    version:[[:space:]]*/ {
      version = $2
      gsub(/^["\047]|["\047]$/, "", version)
      if (version == wanted) { print digest "|" url; exit }
    }
  ' "$helm_index")"
  [[ -n "$chart_record" ]] || die "released odigos-central chart ${chart_version} is not present in ${HELM_REPO_INDEX_URL}"
  chart_digest="${chart_record%%|*}"
  chart_source="${chart_record#*|}"
  [[ "$chart_digest" =~ ^[0-9a-fA-F]{64}$ ]] || die "invalid digest for released chart ${chart_version}"
  [[ "$chart_source" == "${HELM_RELEASE_URL_PREFIX}"* ]] || die "released chart URL is outside the official Odigos GitHub releases: ${chart_source}"
  chart_archive="${cache_root}/charts/${chart_version}/$(basename "$chart_source")"
  download_file "released odigos-central chart ${chart_version}" "$chart_source" "$chart_archive"
  log "Verifying released chart digest from the official Helm index"
  verify_cached_download "released odigos-central chart ${chart_version}" "$chart_source" "$chart_archive" "$chart_digest"
  log "Extracting released Central chart"
  log "  source: ${chart_archive}"
  log "  destination: ${bundle_root}/chart"
  tar -xzf "$chart_archive" -C "${bundle_root}/chart" --strip-components=1
fi

CHART_DIR="${bundle_root}/chart"
chart_name="$(awk '$1 == "name:" { value=$2; gsub(/^["\047]|["\047]$/, "", value); print value; exit }' "${CHART_DIR}/Chart.yaml")"
packaged_chart_version="$(awk '$1 == "version:" { value=$2; gsub(/^["\047]|["\047]$/, "", value); print value; exit }' "${CHART_DIR}/Chart.yaml")"
packaged_app_version="$(awk '$1 == "appVersion:" { value=$2; gsub(/^["\047]|["\047]$/, "", value); print value; exit }' "${CHART_DIR}/Chart.yaml")"
[[ "$chart_name" == "odigos-central" ]] || die "downloaded chart name is ${chart_name}, expected odigos-central"
if [[ "$chart_source" != local:* ]]; then
  [[ "$packaged_chart_version" == "$chart_version" ]] || die "released chart version is ${packaged_chart_version}, expected ${chart_version}"
  [[ "$packaged_app_version" == "$CENTRAL_VERSION" ]] || die "released chart appVersion is ${packaged_app_version}, expected ${CENTRAL_VERSION}"
fi
log "Chart verified: ${chart_name} ${packaged_chart_version} (appVersion ${packaged_app_version})"

release_url_version="${K3S_VERSION//+/%2B}"
k3s_release_url="https://github.com/k3s-io/k3s/releases/download/${release_url_version}"
case "$TARGET_ARCH" in
  amd64)
    k3s_binary_asset="k3s"
    k3s_checksum_asset="sha256sum-amd64.txt"
    ;;
  arm64)
    k3s_binary_asset="k3s-arm64"
    k3s_checksum_asset="sha256sum-arm64.txt"
    ;;
esac
k3s_airgap_asset="k3s-airgap-images-${TARGET_ARCH}.tar.zst"
k3s_cache="${cache_root}/k3s/${K3S_VERSION}/linux-${TARGET_ARCH}"
k3s_install_url="https://raw.githubusercontent.com/k3s-io/k3s/${release_url_version}/install.sh"

log "Downloading K3s ${K3S_VERSION} for linux/${TARGET_ARCH}"
download_file "K3s checksum manifest" "${k3s_release_url}/${k3s_checksum_asset}" "${k3s_cache}/${k3s_checksum_asset}"
download_file "K3s server binary" "${k3s_release_url}/${k3s_binary_asset}" "${k3s_cache}/${k3s_binary_asset}"
download_file "K3s air-gap image archive" "${k3s_release_url}/${k3s_airgap_asset}" "${k3s_cache}/${k3s_airgap_asset}"
download_file "K3s installation script" "$k3s_install_url" "${k3s_cache}/k3s-install.sh"

log "Verifying K3s release checksums"
for verification_record in \
  "${k3s_binary_asset}|${k3s_cache}/${k3s_binary_asset}|${k3s_release_url}/${k3s_binary_asset}|K3s server binary" \
  "${k3s_airgap_asset}|${k3s_cache}/${k3s_airgap_asset}|${k3s_release_url}/${k3s_airgap_asset}|K3s air-gap image archive"; do
  asset="${verification_record%%|*}"
  remainder="${verification_record#*|}"
  path="${remainder%%|*}"
  remainder="${remainder#*|}"
  url="${remainder%%|*}"
  description="${remainder#*|}"
  expected="$(awk -v wanted="$asset" '{ name=$2; sub(/^\*/, "", name); if (name == wanted) { print $1; exit } }' "${k3s_cache}/${k3s_checksum_asset}")"
  [[ -n "$expected" ]] || die "K3s checksum list does not contain ${asset}"
  verify_cached_download "$description" "$url" "$path" "$expected"
done

log "Copying verified K3s payload into the bundle workspace"
cp "${k3s_cache}/${k3s_checksum_asset}" "${build_dir}/${k3s_checksum_asset}"
cp "${k3s_cache}/${k3s_binary_asset}" "${bundle_root}/bin/k3s"
cp "${k3s_cache}/${k3s_airgap_asset}" "${bundle_root}/images/${k3s_airgap_asset}"
cp "${k3s_cache}/k3s-install.sh" "${bundle_root}/bin/k3s-install.sh"
chmod 0755 "${bundle_root}/bin/k3s" "${bundle_root}/bin/k3s-install.sh"

helm_archive="helm-${HELM_VERSION}-linux-${TARGET_ARCH}.tar.gz"
helm_url="https://get.helm.sh/${helm_archive}"
helm_cache="${cache_root}/helm/${HELM_VERSION}/linux-${TARGET_ARCH}"
log "Downloading Helm ${HELM_VERSION} for linux/${TARGET_ARCH}"
download_file "Helm archive" "$helm_url" "${helm_cache}/${helm_archive}"
download_file "Helm checksum" "${helm_url}.sha256sum" "${helm_cache}/${helm_archive}.sha256sum"
helm_expected="$(awk '{print $1; exit}' "${helm_cache}/${helm_archive}.sha256sum")"
[[ "$helm_expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "invalid Helm checksum file for ${helm_archive}"
log "Verifying Helm release checksum"
verify_cached_download "Helm archive" "$helm_url" "${helm_cache}/${helm_archive}" "$helm_expected"
cp "${helm_cache}/${helm_archive}" "${build_dir}/${helm_archive}"
cp "${helm_cache}/${helm_archive}.sha256sum" "${build_dir}/${helm_archive}.sha256sum"
log "Extracting the Helm client"
tar -xzf "${build_dir}/${helm_archive}" -C "$build_dir"
cp "${build_dir}/linux-${TARGET_ARCH}/helm" "${bundle_root}/bin/helm"
chmod 0755 "${bundle_root}/bin/helm"

redis_image="$(chart_image redis)"
keycloak_image="$(chart_image auth)"
[[ -n "$redis_image" ]] || die "could not resolve redis.image from the chart"
[[ -n "$keycloak_image" ]] || die "could not resolve auth.image from the chart"

images=(
  "${IMAGE_PREFIX}/odigos-enterprise-central-backend:${CENTRAL_VERSION}"
  "${IMAGE_PREFIX}/odigos-enterprise-central-ui:${CENTRAL_VERSION}"
  "${IMAGE_PREFIX}/odigos-enterprise-connector-runtime:${CENTRAL_VERSION}"
  "${IMAGE_PREFIX}/odigos-enterprise-connector-aws:${CENTRAL_VERSION}"
  "${IMAGE_PREFIX}/odigos-enterprise-connector-gcp:${CENTRAL_VERSION}"
  "${IMAGE_PREFIX}/odigos-enterprise-connector-azure:${CENTRAL_VERSION}"
  "${IMAGE_PREFIX}/odigos-enterprise-connector-postgres:${CENTRAL_VERSION}"
  "$keycloak_image"
  "$redis_image"
)

if [[ -n "${ODIGOS_ONPREM_TOKEN_FILE:-}" ]]; then
  log "Preparing an isolated Docker credential directory"
  [[ -r "$ODIGOS_ONPREM_TOKEN_FILE" ]] || die "ODIGOS_ONPREM_TOKEN_FILE is not readable"
  temporary_docker_config="${build_dir}/docker-config"
  mkdir -p "$temporary_docker_config"
  existing_docker_config="${DOCKER_CONFIG:-${HOME:-}/.docker}"
  if [[ -d "$existing_docker_config" ]]; then
    cp -R "${existing_docker_config}/." "$temporary_docker_config/"
  fi
  export DOCKER_CONFIG="$temporary_docker_config"
  log "Authenticating to registry.odigos.io (credential contents are not logged)"
  docker login registry.odigos.io --username odigos --password-stdin < "$ODIGOS_ONPREM_TOKEN_FILE" >/dev/null
  log "Registry authentication succeeded"
else
  log "No ODIGOS_ONPREM_TOKEN_FILE supplied; using existing Docker credentials"
fi

printf '%s\n' "${images[@]}" > "${bundle_root}/metadata/images.txt"
combined_image_archive="${bundle_root}/images/odigos-central-images.tar"
docker_archive_supported=true
first_image="${images[0]}"
docker_export_test="${build_dir}/docker-export-test.tar"
log "Testing whether the Docker image store supports offline archive export"
log "Pulling test image: ${first_image}"
docker pull --platform "linux/${TARGET_ARCH}" "$first_image" 2>&1 | tee -a "$BUILD_LOG"
if docker save --output "$docker_export_test" "$first_image" 2>&1 | tee -a "$BUILD_LOG"; then
  log "Docker archive export is supported"
  rm -f "$docker_export_test"
else
  docker_archive_supported=false
  log "Docker archive export is unavailable; this can happen with Docker Desktop's containerd image store"
  rm -f "$docker_export_test"
fi

if $docker_archive_supported; then
  log "Pulling ${#images[@]} Central images for linux/${TARGET_ARCH}"
  : > "${bundle_root}/metadata/docker-image-ids.txt"
  for image in "${images[@]}"; do
    log "Pulling ${image}"
    docker pull --platform "linux/${TARGET_ARCH}" "$image" 2>&1 | tee -a "$BUILD_LOG"
    image_id="$(docker image inspect --format '{{.Id}}' "$image")"
    printf '%s %s\n' "$image" "$image_id" >> "${bundle_root}/metadata/docker-image-ids.txt"
    log "  image ID: ${image_id}"
  done

  log "Writing the combined Central Docker image archive"
  if docker save --output "${combined_image_archive}.part" "${images[@]}" 2>&1 | tee -a "$BUILD_LOG"; then
    mv "${combined_image_archive}.part" "$combined_image_archive"
    log "  result: ${combined_image_archive} ($(file_size "$combined_image_archive") bytes, sha256 $(sha256_file "$combined_image_archive"))"
    image_archive_format="docker-archive"
  else
    docker_archive_supported=false
    log "Combined Docker archive export failed after individual pulls"
    rm -f "${combined_image_archive}.part" "$combined_image_archive"
  fi
fi

if ! $docker_archive_supported; then
  log "Exporting one platform-specific OCI archive per image"
  docker buildx version 2>&1 | tee -a "$BUILD_LOG" >/dev/null || die "Docker Buildx is required for OCI image export fallback"
  build_context="${build_dir}/empty-build-context"
  mkdir -p "$build_context"
  image_number=0
  : > "${bundle_root}/metadata/oci-images.txt"
  for image in "${images[@]}"; do
    image_number=$((image_number + 1))
    printf -v image_archive_name 'odigos-central-image-%02d.oci.tar' "$image_number"
    image_archive="${bundle_root}/images/${image_archive_name}"
    log "Exporting OCI image ${image_number}/${#images[@]}"
    log "  source: ${image}"
    log "  platform: linux/${TARGET_ARCH}"
    log "  destination: ${image_archive}"
    printf 'FROM %s\n' "$image" | docker buildx build \
      --platform "linux/${TARGET_ARCH}" \
      --provenance=false \
      --output "type=oci,dest=${image_archive}.part" \
      --tag "$image" \
      --file - \
      "$build_context" 2>&1 | tee -a "$BUILD_LOG"
    [[ -s "${image_archive}.part" ]] || die "Docker Buildx did not create ${image_archive_name}"
    mv "${image_archive}.part" "$image_archive"
    tar -tf "$image_archive" | grep -qx 'index.json' || die "OCI archive does not contain index.json: ${image_archive_name}"
    image_archive_digest="$(sha256_file "$image_archive")"
    printf '%s %s %s\n' "$image" "$image_archive_name" "$image_archive_digest" >> "${bundle_root}/metadata/oci-images.txt"
    log "  result: completed ($(file_size "$image_archive") bytes, sha256 ${image_archive_digest})"
  done
  image_archive_format="oci-archives"
fi

log "Writing bundle metadata"
cat > "${bundle_root}/metadata/bundle.env" <<EOF
CENTRAL_VERSION='${CENTRAL_VERSION}'
TARGET_ARCH='${TARGET_ARCH}'
K3S_VERSION='${K3S_VERSION}'
HELM_VERSION='${HELM_VERSION}'
IMAGE_PREFIX='${IMAGE_PREFIX}'
CHART_VERSION='${packaged_chart_version}'
CHART_SOURCE='${chart_source}'
CHART_SHA256='${chart_digest}'
CHART_GIT_REVISION='${chart_revision}'
CHART_DIRTY='${chart_dirty}'
IMAGE_ARCHIVE_FORMAT='${image_archive_format}'
EOF

cat > "${bundle_root}/metadata/runtime-cloud-artifacts.txt" <<EOF
# These images are referenced by connector-managed workloads in customer cloud accounts.
# They are not Kubernetes workloads in the odigos-central chart and are therefore not
# imported as Central dependencies. Mirror them into a registry reachable by those workloads
# when the managed AWS/GCP/Azure environment does not have registry egress.
AWS_FARGATE_AGENTS_IMAGE=${AWS_FARGATE_AGENTS_IMAGE}
SERVERLESS_AGENTS_IMAGE=${SERVERLESS_AGENTS_IMAGE}
EOF

log "Generating payload checksums"
: > "${bundle_root}/SHA256SUMS"
while IFS= read -r path; do
  relative="${path#"${bundle_root}/"}"
  digest="$(sha256_file "$path")"
  printf '%s  %s\n' "$digest" "$relative" >> "${bundle_root}/SHA256SUMS"
  log "  checksum: ${relative} (${digest})"
done < <(find "$bundle_root" -type f ! -name SHA256SUMS | LC_ALL=C sort)

mkdir -p "$OUTPUT_DIR"
archive="${OUTPUT_DIR}/${bundle_name}.tar.gz"
archive_partial="${archive}.part"
if [[ -f "$archive" ]]; then
  log "An existing archive will be replaced only after the new archive is complete: ${archive}"
fi
if [[ -e "$archive_partial" ]]; then
  log "Removing stale incomplete archive: ${archive_partial}"
  rm -f "$archive_partial"
fi
log "Creating ${archive}"
tar -czf "$archive_partial" -C "$build_dir" "$bundle_name"
log "Moving the completed archive into place: ${archive_partial} -> ${archive}"
mv "$archive_partial" "$archive"
archive_digest="$(sha256_file "$archive")"
printf '%s  %s\n' "$archive_digest" "$(basename "$archive")" > "${archive}.sha256"
log "Wrote archive checksum file: ${archive}.sha256"

log "Bundle complete"
log "Archive size: $(file_size "$archive") bytes"
log "Archive SHA-256: ${archive_digest}"
log "Build transcript: ${BUILD_LOG}"
printf '%s\n' "$archive"
printf '%s\n' "${archive}.sha256"
