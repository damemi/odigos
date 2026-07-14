# syntax=docker/dockerfile:1
#
# AWS connector image. Self-contained: compiles the connector binary, fetches the official
# open-telemetry/opentelemetry-lambda release artifacts, and assembles a COMPLETE Lambda layer per
# (language, arch) — the language SDK / auto-instrumentation layer combined with the architecture-
# specific OTel collector layer — staged at:
#
#     /var/odigos/lambda/<language>/<arch>/layer.zip      (language: python|nodejs|java|ruby ; arch: x86_64|arm64)
#
# These are every language layer the open-telemetry/opentelemetry-lambda project publishes; there is no
# .NET or Go language layer, so those runtimes are reported as unsupported at instrumentation time.
#
# At runtime the connector publishes the matching zip into the customer's account (when no suitable
# layer already exists there) and attaches it. Bump the *_VERSION ARGs to track new OTel releases.
# A zip committed under connectors/providers/aws/layers/<language>/<arch>/layer.zip overrides the
# assembled one (escape hatch for a custom/Odigos-built layer).
#
# Build from the repository root:
#   docker build -f connectors/providers/aws/Dockerfile .

# --- Go build ------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26.2 AS builder
ARG TARGETARCH
ARG LD_FLAGS
WORKDIR /workspace
COPY connectors/ connectors/
WORKDIR /workspace/connectors/providers/aws
# The odigos-io dependencies (agents-api, odigos/*) are public, so a token is OPTIONAL: when a non-empty
# github_token secret is mounted we rewrite github.com fetches to authenticate (private-repo/rate-limit
# support, matching .devcontainer's :x-oauth-basic form); otherwise we fetch anonymously (module proxy +
# direct git). Injecting an empty/invalid token would make GitHub reject even public fetches, so guard it.
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/connector-aws ./cmd

# --- layer assembly: download official OTel layers and merge SDK + collector per (language, arch) -
FROM alpine:3.20 AS layers
RUN apk add --no-cache curl zip unzip
ARG OTEL_PYTHON_VERSION=0.20.0
ARG OTEL_NODEJS_VERSION=0.22.0
ARG OTEL_JAVA_VERSION=0.20.0
ARG OTEL_RUBY_VERSION=0.14.0
ARG OTEL_COLLECTOR_VERSION=0.22.0
# Standalone OpenTelemetry Java agent (not part of the Lambda javawrapper layer). It is staged at
# /opt/opentelemetry-javaagent.jar so web-server Lambdas (AWS Lambda Web Adapter) can auto-instrument via
# JAVA_TOOL_OPTIONS=-javaagent — the javawrapper layer only handles the RequestHandler-wrapping path.
ARG OTEL_JAVA_AGENT_VERSION=2.27.0
ARG OTEL_BASE=https://github.com/open-telemetry/opentelemetry-lambda/releases/download
ARG OTEL_JAVA_AGENT_BASE=https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download

WORKDIR /work
COPY connectors/providers/aws/collector-config.yaml ./collector-config.yaml
RUN set -eu; \
    echo "downloading OTel Lambda layers"; \
    curl -fsSL "$OTEL_BASE/layer-python/$OTEL_PYTHON_VERSION/opentelemetry-python-layer.zip"        -o python.zip; \
    curl -fsSL "$OTEL_BASE/layer-nodejs/$OTEL_NODEJS_VERSION/opentelemetry-nodejs-layer.zip"        -o nodejs.zip; \
    curl -fsSL "$OTEL_BASE/layer-javaagent/$OTEL_JAVA_VERSION/opentelemetry-javawrapper-layer.zip"  -o java.zip; \
    curl -fsSL "$OTEL_BASE/layer-ruby/$OTEL_RUBY_VERSION/opentelemetry-ruby-layer.zip"              -o ruby.zip; \
    curl -fsSL "$OTEL_JAVA_AGENT_BASE/v$OTEL_JAVA_AGENT_VERSION/opentelemetry-javaagent.jar"        -o opentelemetry-javaagent.jar; \
    curl -fsSL "$OTEL_BASE/layer-collector/$OTEL_COLLECTOR_VERSION/opentelemetry-collector-layer-amd64.zip" -o collector-amd64.zip; \
    curl -fsSL "$OTEL_BASE/layer-collector/$OTEL_COLLECTOR_VERSION/opentelemetry-collector-layer-arm64.zip" -o collector-arm64.zip; \
    combine() { \
      out="/out/var/odigos/lambda/$1/$3"; staging="/tmp/$1-$3"; \
      rm -rf "$staging"; mkdir -p "$staging" "$out"; \
      unzip -oq "$2" -d "$staging"; \
      unzip -oq "$4" -d "$staging"; \
      mkdir -p "$staging/collector-config"; \
      cp collector-config.yaml "$staging/collector-config/config.yaml"; \
      if [ "$1" = "java" ]; then cp opentelemetry-javaagent.jar "$staging/opentelemetry-javaagent.jar"; fi; \
      (cd "$staging" && zip -rqy "$out/layer.zip" .); \
      echo "assembled $out/layer.zip"; \
    }; \
    for lang in python nodejs java ruby; do \
      combine "$lang" "$lang.zip" x86_64 collector-amd64.zip; \
      combine "$lang" "$lang.zip" arm64  collector-arm64.zip; \
    done; \
    find /out/var/odigos/lambda -name '*.zip' -exec ls -lh {} \;

# Optional committed overrides (connectors/providers/aws/layers/<lang>/<arch>/layer.zip win over fetched).
COPY connectors/providers/aws/layers/ /out/var/odigos/lambda/
RUN rm -f /out/var/odigos/lambda/README.md

# --- runtime image -------------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/connector-aws /app
COPY --from=layers /out/var/odigos/lambda /var/odigos/lambda
COPY connectors/providers/aws/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
