#!/usr/bin/env bash
#
# Local dev helper for iterating on enterprise-go-instrumentation against a
# running kind cluster. NOT used by CI.
#
# What it does:
#   1. Resolves ENTERPRISE_GO_BRANCH tip to a pseudo-version via `go list`.
#   2. Rewrites the go.opentelemetry.io/auto replace directive in odiglet/go.mod
#      to point at that pseudo-version and runs `go mod tidy`.
#   3. Bakes the resolved version into deps.mk as the default GOLANG_ENTERPRISE_TAG.
#   4. Builds & loads a new odiglet image into kind and rolls out the daemonset.
#
# Requires: git, go, make, docker, kind, kubectl, perl.
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ODIGLET_DIR="${ODIGLET_DIR:-${ROOT_DIR}/odiglet}"
DEPS_MK="${DEPS_MK:-${ROOT_DIR}/deps.mk}"

AUTO_MODULE_PATH="${AUTO_MODULE_PATH:-go.opentelemetry.io/auto}"
ENTERPRISE_GO_MODULE="${ENTERPRISE_GO_MODULE:-github.com/odigos-io/enterprise-go-instrumentation}"
ENTERPRISE_GO_REMOTE="${ENTERPRISE_GO_REMOTE:-git@github.com:odigos-io/enterprise-go-instrumentation.git}"
ENTERPRISE_GO_BRANCH="${ENTERPRISE_GO_BRANCH:-optimize-code-attributes}"
NAMESPACE="${NAMESPACE:-odigos-system}"
ORG="${ORG:-registry.odigos.io}"
IMG_SUFFIX="${IMG_SUFFIX:-}"
TAG_PREFIX="${TAG_PREFIX:-a}"

export GOPRIVATE="${GOPRIVATE:-${ENTERPRISE_GO_MODULE}}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.0}"

image_repo="${ORG}/odigos-enterprise-odiglet${IMG_SUFFIX}"

for command in git go make docker kind kubectl perl; do
	if ! command -v "${command}" >/dev/null 2>&1; then
		echo "Missing required command: ${command}" >&2
		exit 1
	fi
done

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

ref="refs/heads/${ENTERPRISE_GO_BRANCH}"
commit="$(git ls-remote "${ENTERPRISE_GO_REMOTE}" "${ref}" | awk '{print $1}')"

if [[ -z "${commit}" ]]; then
	echo "Could not find ${ref} in ${ENTERPRISE_GO_REMOTE}" >&2
	exit 1
fi

version_json="$(go list -m -json "${ENTERPRISE_GO_MODULE}@${commit}")"
version="$(printf '%s\n' "${version_json}" | awk -F'"' '/"Version":/ {print $4; exit}')"

if [[ -z "${version}" ]]; then
	echo "Could not resolve pseudo-version for ${ENTERPRISE_GO_MODULE}@${commit}" >&2
	exit 1
fi

(
	cd "${ODIGLET_DIR}"
	GOWORK="${GOWORK:-off}" go mod edit -replace="${AUTO_MODULE_PATH}=${ENTERPRISE_GO_MODULE}@${version}"
	GOWORK="${GOWORK:-off}" go mod tidy
)

GOLANG_ENTERPRISE_VERSION="${version}" perl -0pi -e 's|^GOLANG_ENTERPRISE_TAG := .*$|GOLANG_ENTERPRISE_TAG := \$(if \$(GOLANG_ENTERPRISE_TAG),\$(GOLANG_ENTERPRISE_TAG),$ENV{GOLANG_ENTERPRISE_VERSION})|m' "${DEPS_MK}"

(
	cd "${ROOT_DIR}"
	TAG="${TAG}" ORG="${ORG}" IMG_SUFFIX="${IMG_SUFFIX}" make deploy-odiglet
)

kubectl -n "${NAMESPACE}" set image daemonset/odiglet \
	"odiglet=${image}" \
	"deviceplugin=${image}"

init_container_name="$(kubectl -n "${NAMESPACE}" get daemonset odiglet -o jsonpath='{.spec.template.spec.initContainers[0].name}' 2>/dev/null || true)"
if [[ -n "${init_container_name}" ]]; then
	kubectl -n "${NAMESPACE}" patch daemonset odiglet --type=json \
		-p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/initContainers/0/image\",\"value\":\"${image}\"}]"
fi

kubectl -n "${NAMESPACE}" rollout status daemonset/odiglet

echo "Updated ${AUTO_MODULE_PATH} to ${ENTERPRISE_GO_MODULE}@${version}"
echo "Commit: ${commit}"
echo "Tag: ${TAG}"
