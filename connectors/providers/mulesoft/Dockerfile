# syntax=docker/dockerfile:1
#
# MuleSoft connector image. Compiles the connector-mulesoft binary and ships it on a distroless base. Mule
# instrumentation is configuration only (application properties), so there are no agent artifacts to stage.
#
# Build from the repository root:
#   docker build -f connectors/providers/mulesoft/Dockerfile .

# --- Go build ------------------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26.9 AS builder
ENV GOWORK=off
ARG TARGETARCH
ARG LD_FLAGS
WORKDIR /workspace
COPY connectors/ connectors/
WORKDIR /workspace/connectors/providers/mulesoft
# The agents-api branch is private. The Makefile passes github_token as a BuildKit secret;
# avoid writing an empty credential when running the Dockerfile directly without one.
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/connector-mulesoft ./cmd

# --- runtime image -------------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/connector-mulesoft /app
COPY connectors/providers/mulesoft/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
