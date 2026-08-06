# 1.26.4 rather than OSS's 1.26.2: odigosauth declares `go 1.26.4` (#3038) and the builder runs
# with GOTOOLCHAIN=local.
FROM --platform=$BUILDPLATFORM golang:1.26.4-trixie AS builder
# The auth extension replaces odigosauth, which lives at the repo root rather than under collector/.
# Copied first because it changes far less often than collector/, so the layer stays cached.
COPY ./odigosauth/ /go/src/odigosauth
COPY ./collector/ /go/src/collector
WORKDIR /go/src/collector

# download dependencies before branching out to cross-compile for better caching
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd odigosotelcol && go mod download

ARG TARGETARCH
ARG RHEL=false
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=linux GOARCH=$TARGETARCH make build-odigoscol

RUN if [ "$RHEL" = "true" ] ; then \
      make licenses ; \
    fi

FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS rhel
ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION
LABEL "name"="collector"
LABEL "vendor"="Odigos"
LABEL "maintainer"="Odigos"
LABEL "version"=$VERSION
LABEL "release"=$RELEASE
LABEL "summary"=$SUMMARY
LABEL "description"=$DESCRIPTION
COPY --from=builder /go/src/collector/odigosotelcol/odigosotelcol /odigosotelcol
COPY --from=builder /go/src/collector/odigosotelcol/licenses /licenses
COPY collector/LICENSE /licenses/LICENSE
USER 65532:65532
CMD ["/odigosotelcol"]

FROM gcr.io/distroless/base:latest
COPY --from=builder /go/src/collector/odigosotelcol/odigosotelcol /odigosotelcol
CMD ["/odigosotelcol"]
