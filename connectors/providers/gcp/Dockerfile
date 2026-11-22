# syntax=docker/dockerfile:1
#
# GCP connector image. Compiles the connector-gcp binary and ships it on a distroless base. Unlike the
# AWS image there are no instrumentation artifacts to stage yet (Cloud Run instrumentation is not built),
# so the image is a plain static binary.
#
# Build from the repository root:
#   docker build -f connectors/providers/gcp/Dockerfile .

# --- Go build ------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27.4 AS builder
ENV GOWORK=off
ENV GOPRIVATE=github.com/odigos-io/*
ARG TARGETARCH
ARG LD_FLAGS
WORKDIR /workspace
# The connector modules replace these odigos modules with local paths.
COPY common/ common/
COPY destinations/ destinations/
COPY connectors/ connectors/
WORKDIR /workspace/connectors/providers/gcp
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
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/connector-gcp ./cmd

# --- runtime image -------------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/connector-gcp /app
COPY connectors/providers/gcp/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
