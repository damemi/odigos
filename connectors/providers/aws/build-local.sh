#!/usr/bin/env bash
# Build against an uncommitted sibling agents-api checkout, without publishing dependencies.
set -euo pipefail
repo="$(cd "$(dirname "$0")/../../.." && pwd)"
api="${AGENTS_API_SRC:-$repo/../agents-api}"
image="${CONNECTOR_IMAGE:-odigos-local/connector-aws:dev}"
layers="${LAMBDA_AGENTS_IMAGE:-odigos-local/odigos-lambda-agents:dev}"
arch="${TARGETARCH:-arm64}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
api="$(cd "$api/go/server" && pwd)"
(
  cd "$work"
  GOWORK=off go work init "$api" "$repo/connectors" "$repo/connectors/base" "$repo/connectors/providers/aws"
)
(
  cd "$repo"
  GOWORK="$work/go.work" CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$work/connector-aws" ./connectors/providers/aws/cmd
)
cat > "$work/Dockerfile" <<'DOCKER'
ARG LAMBDA_AGENTS_IMAGE
FROM ${LAMBDA_AGENTS_IMAGE} AS layers
FROM gcr.io/distroless/static:nonroot AS runtime
COPY connector-aws /app
COPY --from=layers /var/odigos/lambda /var/odigos/lambda
COPY LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
DOCKER
cp "$repo/connectors/providers/aws/LICENSE" "$work/LICENSE"
build_args=(--platform "linux/$arch" --build-arg "LAMBDA_AGENTS_IMAGE=$layers")
docker build "${build_args[@]}" -t "$image" "$work"
