# syntax=docker/dockerfile:1
#
# Connector-runtime controller image.
#
# Build from the repository root:
#   docker build -f connector-runtime/Dockerfile .

FROM --platform=$BUILDPLATFORM golang:1.26.9 AS builder
ENV GOWORK=off
ARG TARGETARCH
ARG LD_FLAGS

WORKDIR /workspace
# connector-runtime replaces these modules with local paths.
COPY api/ api/
COPY connectors/ connectors/
COPY connector-runtime/ connector-runtime/
WORKDIR /workspace/connector-runtime
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" \
    -a -o /out/connector-runtime ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/connector-runtime /app
COPY connector-runtime/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
