#!/usr/bin/env bash
# Shared Odigos helm install script for e2e tests.
# Usage: helm-install.sh [extra-helm-set-args...]
#
# Required env vars: IMAGE_TAG, ODIGOS_API_KEY
# The platform-k8s workflow sets ODIGOS_CHART and ODIGOS_HELM_VALUES to the chart and images it built on Depot.
# All other helm flags (features, crons, modes, etc.) are passed by the caller, e.g.:
#   helm-install.sh --set someFeature=true --set goAutoOffsetsCron="0 */6 * * *"

set -e

helm upgrade --install odigos \
  "${ODIGOS_CHART:-../../../../helm/odigos}" \
  ${ODIGOS_HELM_VALUES:+-f "$ODIGOS_HELM_VALUES"} \
  --create-namespace \
  --namespace odigos-test \
  --set image.tag="$IMAGE_TAG" \
  --set onPremToken="$ODIGOS_API_KEY" \
  "$@"

kubectl label namespace odigos-test odigos.io/system-object="true"
