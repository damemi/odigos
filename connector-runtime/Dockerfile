# syntax=docker/dockerfile:1
#
# Connector-runtime controller image.
#
# Build from the repository root:
#   docker build -f connector-runtime/Dockerfile .

FROM --platform=$BUILDPLATFORM golang:1.27.4 AS builder
ENV GOWORK=off
ENV GOPRIVATE=github.com/odigos-io/*
ARG TARGETARCH
ARG LD_FLAGS

WORKDIR /workspace
# connector-runtime replaces these modules with local paths.
COPY api/ api/
COPY connectors/ connectors/
COPY connector-runtime/ connector-runtime/
WORKDIR /workspace/connector-runtime
# The agents-api module is private, so the github_token BuildKit secret is required (the Makefile and the
# bake file pass it from GITHUB_TOKEN). required=true only catches a missing secret; a secret declared from
# an unset variable arrives empty, hence the test. git gets the token only as per-command config, so it
# is never written into a layer.
RUN --mount=type=secret,id=github_token,env=GITHUB_TOKEN,required=true \
    --mount=type=cache,target=/go/pkg \
    test -n "$GITHUB_TOKEN" || { echo "The github_token secret is empty; set GITHUB_TOKEN." >&2; exit 1; } && \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:${GITHUB_TOKEN}@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token,env=GITHUB_TOKEN,required=true \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    test -n "$GITHUB_TOKEN" || { echo "The github_token secret is empty; set GITHUB_TOKEN." >&2; exit 1; } && \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0="url.https://x-access-token:${GITHUB_TOKEN}@github.com/.insteadOf" \
    GIT_CONFIG_VALUE_0="https://github.com/" \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" \
    -a -o /out/connector-runtime ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/connector-runtime /app
COPY connector-runtime/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
