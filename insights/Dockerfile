FROM --platform=$BUILDPLATFORM golang:1.27.4 AS builder

WORKDIR /workspace
# insights replaces these odigos modules with local paths.
COPY common/ common/
COPY odigosauth/ odigosauth/
COPY insights/ insights/
WORKDIR /workspace/insights
RUN --mount=type=cache,target=/go/pkg \
    go mod download && go mod verify
ARG TARGETARCH
ARG LD_FLAGS
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/build/insights ./cmd

######## RHEL Image ########
FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS rhel
ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION
LABEL "name"="insights"
LABEL "vendor"="Odigos"
LABEL "maintainer"="Odigos"
LABEL "version"=$VERSION
LABEL "release"=$RELEASE
LABEL "summary"=$SUMMARY
LABEL "description"=$DESCRIPTION

WORKDIR /
COPY --from=builder /workspace/build/insights ./app
COPY --from=builder /workspace/insights/LICENSE ./licenses/.
USER 65532:65532
ENTRYPOINT ["/app"]

######### Final Image #########
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/build/insights ./app
USER 65532:65532
ENTRYPOINT ["/app"]
