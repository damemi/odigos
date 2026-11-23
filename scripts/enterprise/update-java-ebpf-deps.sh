#!/usr/bin/env bash
#
# Local dev helper for iterating on ebpf-java-instrumentation against a running
# kind cluster. NOT used by CI.
#
# What it does:
#   1. Resolves JAVA_EBPF_BRANCH tip on the remote to pseudo-versions via `go list`
#      (the image build runs `go mod download`, so the dependency must be reachable
#      from the remote — a local-path replace would not work inside Docker).
#   2. Rewrites the two ebpf-java-instrumentation require directives in
#      odiglet/go.mod (root module + otel_agent_extension submodule) and runs
#      `go mod tidy`. This only updates the odiglet-side Go code compiled into the
#      odiglet binary.
#   3. Builds the two Java agent artifact images (java-ebpf-instrumentations and
#      java-enterprise) from the LOCAL checkout's working tree and rewrites the
#      `COPY --from=...` refs in odiglet/Dockerfile to use them. Without this the
#      instrumentation artifacts injected into the JVM (tracing_probes.so,
#      javaagent.jar, otel_agent_extension.jar, end_span_usdt.so, ...) stay pinned
#      to the published release tag and the deployed Java instrumentation is stale.
#      The Dockerfile edit is reverted on exit. NOTE: unlike the go.mod bump (which
#      uses the pushed remote commit), the agent images are built from the local
#      working tree, so unpushed/uncommitted agent changes ARE included.
#   4. Builds a new odiglet image, loads it into the target kind cluster and rolls
#      out the daemonset using a specific kubeconfig.
#
# Requires: git, go, make, docker, kind, kubectl.
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ODIGLET_DIR="${ODIGLET_DIR:-${ROOT_DIR}/odiglet}"
DOCKERFILE_PATH="${ODIGLET_DIR}/Dockerfile"

# Single cleanup hook: restore odiglet/Dockerfile and remove the temp image
# archive regardless of how the script exits.
cleanup() {
	if [[ -n "${DOCKERFILE_BACKUP:-}" && -f "${DOCKERFILE_BACKUP}" ]]; then
		mv -f "${DOCKERFILE_BACKUP}" "${DOCKERFILE_PATH}"
	fi
	[[ -n "${image_archive:-}" ]] && rm -f "${image_archive}"
}
trap cleanup EXIT

# Local checkout of ebpf-java-instrumentation (used only to auto-detect the branch
# and to warn about unpushed commits). Override with JAVA_EBPF_LOCAL_DIR.
JAVA_EBPF_LOCAL_DIR="${JAVA_EBPF_LOCAL_DIR:-${ROOT_DIR}/../ebpf-java-instrumentation}"

JAVA_MODULE_ROOT="${JAVA_MODULE_ROOT:-github.com/odigos-io/ebpf-java-instrumentation/v2}"
JAVA_MODULE_EXT="${JAVA_MODULE_EXT:-github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/v2}"
JAVA_EBPF_REMOTE="${JAVA_EBPF_REMOTE:-git@github.com:odigos-io/ebpf-java-instrumentation.git}"

# Branch to deploy. Defaults to whatever is checked out in JAVA_EBPF_LOCAL_DIR,
# falling back to exception-stack-traces.
if [[ -z "${JAVA_EBPF_BRANCH:-}" ]]; then
	if [[ -d "${JAVA_EBPF_LOCAL_DIR}/.git" ]]; then
		JAVA_EBPF_BRANCH="$(git -C "${JAVA_EBPF_LOCAL_DIR}" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
	fi
	JAVA_EBPF_BRANCH="${JAVA_EBPF_BRANCH:-exception-stack-traces}"
fi

KUBECONFIG_PATH="${KUBECONFIG_PATH:-/tmp/kind-kubeconfig-kind-opus-dt-starter.yaml}"
NAMESPACE="${NAMESPACE:-odigos-system}"
ORG="${ORG:-registry.odigos.io}"
IMG_SUFFIX="${IMG_SUFFIX:-}"
TAG_PREFIX="${TAG_PREFIX:-j}"

export GOPRIVATE="${GOPRIVATE:-github.com/odigos-io}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.0}"

image_repo="${ORG}/odigos-enterprise-odiglet${IMG_SUFFIX}"

for command in git go make docker kind kubectl; do
	if ! command -v "${command}" >/dev/null 2>&1; then
		echo "Missing required command: ${command}" >&2
		exit 1
	fi
done

if [[ ! -f "${KUBECONFIG_PATH}" ]]; then
	echo "Kubeconfig not found: ${KUBECONFIG_PATH}" >&2
	exit 1
fi
export KUBECONFIG="${KUBECONFIG_PATH}"

# Derive the kind cluster name from the kubeconfig current-context.
# kind contexts are named "kind-<clusterName>".
context="$(kubectl config current-context)"
cluster_name="${KIND_CLUSTER_NAME:-${context#kind-}}"
echo "Using kubeconfig: ${KUBECONFIG_PATH}"
echo "Context: ${context}  ->  kind cluster: ${cluster_name}"

next_tag() {
	local max=0
	local found_tag=false
	local tag
	local number
	local images

	images="$(
		{
			docker images --format '{{.Repository}}:{{.Tag}}' "${image_repo}" 2>/dev/null || true
			kubectl -n "${NAMESPACE}" get daemonset odiglet -o jsonpath='{range .spec.template.spec.initContainers[*]}{.image}{"\n"}{end}{range .spec.template.spec.containers[*]}{.image}{"\n"}{end}' 2>/dev/null || true
		}
	)"

	while IFS= read -r image; do
		tag="${image##*:}"
		if [[ "${tag}" =~ ^${TAG_PREFIX}([0-9]+)$ ]]; then
			found_tag=true
			number="${BASH_REMATCH[1]}"
			if ((10#${number} > max)); then
				max="$((10#${number}))"
			fi
		fi
	done <<< "${images}"

	if [[ "${found_tag}" == false ]]; then
		printf '%s1\n' "${TAG_PREFIX}"
		return
	fi

	printf '%s%s\n' "${TAG_PREFIX}" "$((max + 1))"
}

TAG="${TAG:-$(next_tag)}"
image="${image_repo}:${TAG}"
echo "Using tag: ${TAG}"

# Resolve the branch tip on the remote.
ref="refs/heads/${JAVA_EBPF_BRANCH}"
commit="$(git ls-remote "${JAVA_EBPF_REMOTE}" "${ref}" | awk '{print $1}')"
if [[ -z "${commit}" ]]; then
	echo "Could not find ${ref} in ${JAVA_EBPF_REMOTE}" >&2
	exit 1
fi
echo "Remote ${JAVA_EBPF_BRANCH} -> ${commit}"

# Warn if the local checkout has commits that were not pushed: the image build
# uses the remote, so unpushed work will NOT be included.
if [[ -d "${JAVA_EBPF_LOCAL_DIR}/.git" ]]; then
	local_head="$(git -C "${JAVA_EBPF_LOCAL_DIR}" rev-parse HEAD 2>/dev/null || true)"
	if [[ -n "${local_head}" && "${local_head}" != "${commit}" ]]; then
		echo "⚠️  Local ${JAVA_EBPF_LOCAL_DIR} HEAD (${local_head:0:12}) differs from remote ${JAVA_EBPF_BRANCH} (${commit:0:12})." >&2
		echo "⚠️  Push your branch so the image build picks up the latest commit: git -C ${JAVA_EBPF_LOCAL_DIR} push origin ${JAVA_EBPF_BRANCH}" >&2
	fi
fi

resolve_version() {
	local module="$1"
	go list -m -json "${module}@${commit}" | awk -F'"' '/"Version":/ {print $4; exit}'
}

version_root="$(resolve_version "${JAVA_MODULE_ROOT}")"
version_ext="$(resolve_version "${JAVA_MODULE_EXT}")"
if [[ -z "${version_root}" || -z "${version_ext}" ]]; then
	echo "Could not resolve pseudo-versions for the java modules at ${commit}" >&2
	exit 1
fi
echo "${JAVA_MODULE_ROOT} -> ${version_root}"
echo "${JAVA_MODULE_EXT}  -> ${version_ext}"

(
	cd "${ODIGLET_DIR}"
	# Drop any local-path replace overrides (would break the Docker build context).
	GOWORK="${GOWORK:-off}" go mod edit -dropreplace="${JAVA_MODULE_ROOT}"
	GOWORK="${GOWORK:-off}" go mod edit -dropreplace="${JAVA_MODULE_EXT}"
	GOWORK="${GOWORK:-off}" go mod edit -require="${JAVA_MODULE_ROOT}@${version_root}"
	GOWORK="${GOWORK:-off}" go mod edit -require="${JAVA_MODULE_EXT}@${version_ext}"
	GOWORK="${GOWORK:-off}" go mod tidy
)

# ---------------------------------------------------------------------------
# Build the Java agent artifact images from the local checkout and point
# odiglet/Dockerfile at them.
#
# The go.mod bump above only updates the odiglet-side Go code. The artifacts
# actually injected into the JVM are baked into the odiglet image via
# `COPY --from=<published agent image>` in odiglet/Dockerfile, pinned to a
# release tag. We rebuild both images locally (working tree of JAVA_EBPF_BRANCH)
# so the deployed instrumentation matches the branch:
#   java-ebpf-instrumentations -> /instrumentations/java-ebpf
#       (tracing_probes.so, dtrace-injector*.jar, utils*.jar)
#   java-enterprise            -> /instrumentations/java-ext-ebpf
#       (javaagent.jar, otel_agent_extension.jar, end_span_usdt.so, ...)
# Neither build needs a GitHub token (no private go-module fetch).
# ---------------------------------------------------------------------------
if [[ ! -d "${JAVA_EBPF_LOCAL_DIR}" ]]; then
	echo "JAVA_EBPF_LOCAL_DIR not found: ${JAVA_EBPF_LOCAL_DIR}" >&2
	echo "It is required to build the Java agent artifacts. Set JAVA_EBPF_LOCAL_DIR to your ebpf-java-instrumentation checkout." >&2
	exit 1
fi

java_ebpf_img="local/java-ebpf-instrumentations:${TAG}"
java_ent_img="local/java-enterprise:${TAG}"

echo "Building Java agent images from ${JAVA_EBPF_LOCAL_DIR} (working tree of '${JAVA_EBPF_BRANCH}')"
docker build \
	-t "${java_ebpf_img}" \
	-f "${JAVA_EBPF_LOCAL_DIR}/Dockerfile.java-ebpf-instrumentations" \
	"${JAVA_EBPF_LOCAL_DIR}"
docker build \
	-t "${java_ent_img}" \
	-f "${JAVA_EBPF_LOCAL_DIR}/otel_agent_extension/Dockerfile.java-enterprise" \
	"${JAVA_EBPF_LOCAL_DIR}/otel_agent_extension"

# Rewrite the agent COPY refs in odiglet/Dockerfile for this build only; the
# original is restored on exit by cleanup().
DOCKERFILE_BACKUP="$(mktemp -t odiglet-dockerfile-XXXXXX)"
cp "${DOCKERFILE_PATH}" "${DOCKERFILE_BACKUP}"
rewritten="$(mktemp -t odiglet-dockerfile-new-XXXXXX)"
sed -E \
	-e "s|public\.ecr\.aws/odigos/agents/java-ebpf-instrumentations:[^[:space:]]+|${java_ebpf_img}|g" \
	-e "s|public\.ecr\.aws/odigos/agents/java-enterprise:[^[:space:]]+|${java_ent_img}|g" \
	"${DOCKERFILE_PATH}" > "${rewritten}"
mv "${rewritten}" "${DOCKERFILE_PATH}"

if grep -q "public.ecr.aws/odigos/agents/java-" "${DOCKERFILE_PATH}"; then
	echo "⚠️  Some Java agent COPY refs in ${DOCKERFILE_PATH} were not rewritten:" >&2
	grep -n "public.ecr.aws/odigos/agents/java-" "${DOCKERFILE_PATH}" >&2
fi
echo "Rewrote Java agent images in odiglet/Dockerfile:"
echo "  java-ebpf-instrumentations -> ${java_ebpf_img}"
echo "  java-enterprise            -> ${java_ent_img}"

# Build the odiglet image (bakes in the local agent artifacts via COPY --from).
(
	cd "${ROOT_DIR}"
	TAG="${TAG}" ORG="${ORG}" IMG_SUFFIX="${IMG_SUFFIX}" make build-odiglet
)

# Load into the target kind cluster.
# The cluster may run on either the docker or podman kind provider, and the image
# was built with docker. Detect the provider, then load via an image-archive so the
# transfer works even when the build runtime (docker) differs from the cluster
# runtime (podman).
kind_provider=""
if kind get clusters 2>/dev/null | grep -qx "${cluster_name}"; then
	kind_provider="docker"
elif KIND_EXPERIMENTAL_PROVIDER=podman kind get clusters 2>/dev/null | grep -qx "${cluster_name}"; then
	kind_provider="podman"
else
	echo "kind cluster '${cluster_name}' not found via docker or podman provider." >&2
	echo "Is the cluster running? (kind get clusters)" >&2
	exit 1
fi
echo "kind provider: ${kind_provider}"

image_archive="$(mktemp -t odiglet-XXXXXX).tar"
docker save "${image}" -o "${image_archive}"
KIND_EXPERIMENTAL_PROVIDER="${kind_provider}" kind load image-archive "${image_archive}" --name "${cluster_name}"

# Roll out the new image on the daemonset.
kubectl -n "${NAMESPACE}" set image daemonset/odiglet \
	"odiglet=${image}" \
	"deviceplugin=${image}"

init_container_name="$(kubectl -n "${NAMESPACE}" get daemonset odiglet -o jsonpath='{.spec.template.spec.initContainers[0].name}' 2>/dev/null || true)"
if [[ -n "${init_container_name}" ]]; then
	kubectl -n "${NAMESPACE}" patch daemonset odiglet --type=json \
		-p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/initContainers/0/image\",\"value\":\"${image}\"}]"
fi

kubectl -n "${NAMESPACE}" rollout status daemonset/odiglet

echo "Updated ${JAVA_MODULE_ROOT} to ${version_root}"
echo "Updated ${JAVA_MODULE_EXT} to ${version_ext}"
echo "Java agent images built from local working tree: ${java_ebpf_img}, ${java_ent_img}"
echo "Branch: ${JAVA_EBPF_BRANCH}  Commit: ${commit}"
echo "Cluster: ${cluster_name}  Tag: ${TAG}"
