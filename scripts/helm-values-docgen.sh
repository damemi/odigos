#!/usr/bin/env bash

# Copies each chart's values.yaml into a docs snippet, so the docs show the
# default values of the chart in this repo. CI fails when the snippets are stale.

set -euo pipefail

cd "$(dirname "$0")/.."

CHARTS=("odigos" "odigos-central")
OUT_DIR="docs/snippets/enterprise/helm-values"

mkdir -p "$OUT_DIR"

for chart in "${CHARTS[@]}"; do
	values="helm/${chart}/values.yaml"
	out="${OUT_DIR}/${chart}.mdx"
	if grep -q '```' "$values"; then
		echo "${values} contains a code fence, which would break the generated docs" >&2
		exit 1
	fi
	{
		echo "{/* Generated from ${values} by 'make helm-values-docs'. Do not edit. */}"
		echo
		echo '```yaml'
		cat "$values"
		echo '```'
	} >"$out"
done
