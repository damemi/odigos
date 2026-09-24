# syntax=docker/dockerfile:1
#
# Connector-runtime controller image.
#
# Build from the repository root (requires a sibling odigos checkout for local-path replaces in
# go.mod until odigos/api is published with OdigosCloudConnector):
#   docker build -f connector-runtime/Dockerfile --build-context odigos=../odigos .

FROM --platform=$BUILDPLATFORM golang:1.26.8 AS builder
ARG TARGETARCH
ARG LD_FLAGS
WORKDIR /workspace
COPY connector-runtime/ connector-runtime/
COPY connectors/ connectors/
# go.mod uses local-path replaces relative to the module dir: `../connectors` -> /workspace/connectors
# and `../../odigos/{api,common}` -> /odigos/{api,common}. Place the odigos checkout at /odigos so the
# replace resolves (the build flattens connector-runtime to /workspace/connector-runtime).
COPY --from=odigos . /odigos/
WORKDIR /workspace/connector-runtime
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    go mod download && go mod verify
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /out/connector-runtime ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /out/connector-runtime /app
COPY connector-runtime/LICENSE /licenses/LICENSE
USER 65532:65532
ENTRYPOINT ["/app"]
