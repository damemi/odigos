FROM --platform=$BUILDPLATFORM golang:1.26.9 AS builder

WORKDIR /workspace
# instrumentor/enterprise replaces these odigos modules with local paths.
COPY actions/ actions/
COPY api/ api/
COPY common/ common/
COPY destinations/ destinations/
COPY distros/ distros/
COPY k8sutils/ k8sutils/
COPY odigosauth/ odigosauth/
COPY profiles/ profiles/
COPY recommendations/ recommendations/
COPY status/ status/
COPY instrumentor/ instrumentor/
WORKDIR /workspace/instrumentor/enterprise
RUN --mount=type=cache,target=/go/pkg \
    go mod download && go mod verify
ARG TARGETARCH
ARG LD_FLAGS
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/build/instrumentor ./cmd

######## RHEL Image ########
FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS rhel
ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION
LABEL "name"="instrumentor"
LABEL "vendor"="Odigos"
LABEL "maintainer"="Odigos"
LABEL "version"=$VERSION
LABEL "release"=$RELEASE
LABEL "summary"=$SUMMARY
LABEL "description"=$DESCRIPTION

WORKDIR /
COPY --from=builder /workspace/build/instrumentor ./app
COPY --from=builder /workspace/instrumentor/enterprise/LICENSE ./licenses/.
USER 65532:65532
ENTRYPOINT ["/app"]

######### Final Image #########
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/build/instrumentor ./app
USER 65532:65532
ENTRYPOINT ["/app"]
