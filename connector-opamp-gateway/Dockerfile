# syntax=docker/dockerfile:1
#
# connector-opamp-gateway image: the public OpAMP endpoint every remotely managed Odigos Collector
# connects back to.
#
# Build from the repository root:
#   docker build -f connector-opamp-gateway/Dockerfile .

FROM --platform=$BUILDPLATFORM golang:1.26.2 AS builder
ARG TARGETARCH
ARG LD_FLAGS
WORKDIR /workspace
# connector-opamp-gateway replaces these modules with local paths.
COPY api/ api/
COPY connectors/ connectors/
COPY connector-opamp-gateway/ connector-opamp-gateway/
WORKDIR /workspace/connector-opamp-gateway
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /out/connector-opamp-gateway ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/connector-opamp-gateway /app
COPY connector-opamp-gateway/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
