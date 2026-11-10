# syntax=docker/dockerfile:1
#
# GCP connector image. Compiles the connector-gcp binary and ships it on a distroless base. Unlike the
# AWS image there are no instrumentation artifacts to stage yet (Cloud Run instrumentation is not built),
# so the image is a plain static binary.
#
# Build from the repository root:
#   docker build -f connectors/providers/gcp/Dockerfile .

# --- Go build ------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26.9 AS builder
ENV GOWORK=off
ARG TARGETARCH
ARG LD_FLAGS
WORKDIR /workspace
# The connector modules replace these odigos modules with local paths.
COPY common/ common/
COPY destinations/ destinations/
COPY connectors/ connectors/
WORKDIR /workspace/connectors/providers/gcp
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/connector-gcp ./cmd

# --- runtime image -------------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/connector-gcp /app
COPY connectors/providers/gcp/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
