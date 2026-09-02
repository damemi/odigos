#!/usr/bin/env bash
# Build + push insights-k6-runner to public ECR (single arch — fast).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
REGISTRY="${REGISTRY:-public.ecr.aws/e1u7m2k3}"
IMAGE="${IMAGE:-insights-k6-runner}"
TAG="${TAG:-v0.0.3}"
# EKS nodes are usually amd64; override with PLATFORM=linux/arm64 for Graviton.
PLATFORM="${PLATFORM:-linux/amd64}"
FULL="${REGISTRY}/${IMAGE}:${TAG}"
LATEST="${REGISTRY}/${IMAGE}:latest"

echo "Logging into public.ecr.aws ..."
aws ecr-public get-login-password --region us-east-1 \
  | docker login --username AWS --password-stdin public.ecr.aws

echo "Building + pushing ${FULL} (${PLATFORM}) ..."
docker buildx build \
  --platform "${PLATFORM}" \
  -f "${ROOT}/Dockerfile" \
  -t "${FULL}" \
  -t "${LATEST}" \
  --push \
  "${ROOT}"

echo "Pushed:"
echo "  ${FULL}"
echo "  ${LATEST}"
