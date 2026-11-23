# syntax=docker/dockerfile:1
#
# AWS connector image. Compiles the connector binary and stages the OpenTelemetry Lambda layers at:
#
#     /var/odigos/lambda/<language>/<arch>/layer.zip      (language: python|nodejs|java|ruby ; arch: x86_64|arm64)
#
# At runtime the connector publishes the matching zip into the customer's account (when no suitable layer
# already exists there) and attaches it.
#
# THE LAYERS ARE NOT BUILT HERE. They are built, versioned and released by
# odigos-io/aws-connector-artifacts, which publishes them as odigos-lambda-agents:<version> and — from the
# SAME zips, in the same release — as public Lambda layer versions in every region and as Serverless
# Application Repository applications customers can deploy into their own accounts.
#
# That is the reason for the move rather than a preference about where build logic lives: a customer who
# deploys the layer from SAR and a customer whose functions the connector instruments have to get
# identical bytes, and the only way to guarantee that is for both to come from one build. Assembling a
# second copy here would make it a hope.
#
# Bumping LAMBDA_AGENTS_IMAGE below is how this connector picks up new OpenTelemetry layer versions;
# the upstream versions themselves live in that repository's versions.env.
#
# Build from the repository root:
#   docker build -f connectors/providers/aws/Dockerfile .

# The Lambda layer bundle, pinned to a released tag of odigos-io/aws-connector-artifacts. Declared here,
# in the GLOBAL scope before any FROM, because an ARG declared after a FROM belongs to that build stage
# and cannot be interpolated into a later FROM line.
#
# PIN IT. A floating tag would make the connector image's layer contents depend on when it was built
# rather than on what it says, and "which layer does connector vX ship?" would stop having an answer.
#
# THE VERSION MUST MATCH ARTIFACTS_VERSION in this directory, which is the single place the artifacts
# release this connector is built against is written down. The repo-root Makefile reads that file and
# passes this build arg, so a `make build-connector-aws` never uses the default below; the default exists
# only so a bare `docker build -f connectors/providers/aws/Dockerfile .` still works. TestArtifactVersions
# in pkg/config fails the build if the two drift.
ARG LAMBDA_AGENTS_IMAGE=registry.odigos.io/odigos-lambda-agents:v0.0.30

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
COPY distros/ distros/
COPY connectors/ connectors/
WORKDIR /workspace/connectors/providers/aws
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
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/connector-aws ./cmd

# --- lambda layers: the released artifact bundle --------------------------------------------------
# Carries /var/odigos/lambda/<language>/<arch>/layer.zip and nothing else that matters here; the base is
# busybox purely so the image can be inspected by hand. See LAMBDA_AGENTS_IMAGE at the top of this file.
FROM ${LAMBDA_AGENTS_IMAGE} AS layers

# --- runtime image -------------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot AS runtime
WORKDIR /
COPY --from=builder /workspace/connector-aws /app
COPY --from=layers /var/odigos/lambda /var/odigos/lambda
COPY connectors/providers/aws/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]

FROM runtime AS default
