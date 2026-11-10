#!/usr/bin/env bash
# Shared Odigos helm install script for e2e tests.
# Usage: helm-install.sh [extra-helm-set-args...]
#
# Required env vars: IMAGE_TAG, ODIGOS_API_KEY
# All other helm flags (features, crons, modes, etc.) are passed by the caller, e.g.:
#   helm-install.sh --set someFeature=true --set goAutoOffsetsCron="0 */6 * * *"

set -e

helm upgrade --install odigos \
  ../../../../helm/odigos \
  --create-namespace \
  --namespace odigos-test \
  --set image.tag="$IMAGE_TAG" \
  --set onPremToken="$ODIGOS_API_KEY" \
  "$@"

kubectl label namespace odigos-test odigos.io/system-object="true"
