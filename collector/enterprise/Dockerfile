# 1.26.4 rather than OSS's 1.26.2: odigosauth declares `go 1.26.4` (#3038) and the builder runs
# with GOTOOLCHAIN=local.
FROM --platform=$BUILDPLATFORM golang:1.26.4-trixie AS builder
# install the required tooling for calling `setcap` on the compiled binary
RUN apt-get update && apt-get install -y --no-install-recommends libcap2-bin
# The auth extension replaces odigosauth, which lives at the repo root rather than under collector/.
# Copied first because it changes far less often than collector/, so the layer stays cached.
COPY ./odigosauth/ /go/src/odigosauth
COPY ./collector/ /go/src/collector
# The ebpf receiver and the vm profile attrs processor replace it by relative path, so it has to
# sit next to the collector tree rather than inside it.
COPY ./ebpfshare/ /go/src/ebpfshare
WORKDIR /go/src/collector

# download dependencies before branching out to cross-compile for better caching
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd odigosotelcol && go mod download
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd cmd/odigosopampsupervisor && go mod download

ARG TARGETARCH
ARG RHEL=false
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH make build-odigoscol
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make build-opamp-supervisor TARGET_OS=linux TARGET_ARCH=$TARGETARCH

# Add file capabilities so the binary can use eBPF privileges when run as non-root.
# Caps match odiglet.dataCollection.capabilities.
# only setting the capabilities as permitted (p) so that the binary can be shared between the node collector (needs eBPF) and the cluster collector (does not need eBPF).
# this means that the node collector needs to set its effective capabilities to match the permitted ones, which is done by the odigoscapabilitiesextension.
RUN setcap cap_sys_admin,cap_bpf,cap_perfmon,cap_ipc_lock=p ./odigosotelcol/odigosotelcol

# Seed a writable directory for Supervisor state. Managed deployments should mount persistent
# storage here so the instance identity and last-known-good remote config survive replacement.
RUN mkdir -p /go/src/image-root/var/lib/odigos/collector-supervisor && \
    touch /go/src/image-root/var/lib/odigos/collector-supervisor/.keep

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
COPY --from=builder /go/src/collector/cmd/odigosopampsupervisor/odigosopampsupervisor /odigosopampsupervisor
COPY --chown=65532:65532 collector/managed/ /etc/odigos/managed-collector/
COPY --from=builder --chown=65532:65532 /go/src/image-root/var/lib/odigos/collector-supervisor/ /var/lib/odigos/collector-supervisor/
COPY --from=builder /go/src/collector/odigosotelcol/licenses /licenses
COPY collector/LICENSE /licenses/LICENSE
USER 65532:65532
CMD ["/odigosotelcol"]

FROM gcr.io/distroless/base:latest
COPY --from=builder /go/src/collector/odigosotelcol/odigosotelcol /odigosotelcol
COPY --from=builder /go/src/collector/cmd/odigosopampsupervisor/odigosopampsupervisor /odigosopampsupervisor
COPY --chown=65532:65532 collector/managed/ /etc/odigos/managed-collector/
COPY --from=builder --chown=65532:65532 /go/src/image-root/var/lib/odigos/collector-supervisor/ /var/lib/odigos/collector-supervisor/
# make sure we run as non root user
# this is required when this container is added to a pod that has runAsNonRoot: true
USER 65532:65532
# Existing deployments continue to run the Collector directly. Cloud connector deployments opt in
# to remote management by overriding the command with /odigosopampsupervisor and the bundled config.
CMD ["/odigosotelcol"]
