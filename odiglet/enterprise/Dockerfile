ARG ODIGLET_BASE_IMAGE=registry.odigos.io/odiglet-base:v1.22

######### dotnet builder #########
FROM --platform=$BUILDPLATFORM busybox:1.36.1 AS dotnet-builder
WORKDIR /dotnet-instrumentation
ARG DOTNET_OTEL_VERSION=v1.9.0
ARG TARGETARCH
# SHA256 pins for the artifacts fetched in this stage. Verification runs between the
# download and the unpack, so nothing unverified is ever extracted. Refresh these
# together with DOTNET_OTEL_VERSION — a mismatched download fails the build.
RUN if [ "$TARGETARCH" = "arm64" ]; then \
    echo "arm64" > /tmp/arch_suffix; \
    else \
    echo "x64" > /tmp/arch_suffix; \
    fi
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    case "$ARCH_SUFFIX" in \
      x64) echo "58b6a7e5282ffba6d2f8e09c69e412711050e49a36c82d31edfd84cad3afac49  opentelemetry-dotnet-instrumentation-linux-glibc-x64.zip" | sha256sum -c - ;; \
      arm64) echo "7600d3e1f57d06995d637c95650d9ee28b6e32cf2aa362fdb1a988267425191f  opentelemetry-dotnet-instrumentation-linux-glibc-arm64.zip" | sha256sum -c - ;; \
      *) echo "no checksum pinned for arch $ARCH_SUFFIX" && exit 1 ;; \
    esac && \
    unzip opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    rm opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    mv linux-$ARCH_SUFFIX linux-glibc
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    case "$ARCH_SUFFIX" in \
      x64) echo "c0bc22bac5b5aa8b345cb995e87c39e080025ed5923a01d45f9d61d49c60b301  opentelemetry-dotnet-instrumentation-linux-musl-x64.zip" | sha256sum -c - ;; \
      arm64) echo "a5494eaeeab99f298b3c875dfbac648c0e56c7f04ebc062800904cbb18251229  opentelemetry-dotnet-instrumentation-linux-musl-arm64.zip" | sha256sum -c - ;; \
      *) echo "no checksum pinned for arch $ARCH_SUFFIX" && exit 1 ;; \
    esac && \
    unzip -o opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    rm opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    mv linux-musl-$ARCH_SUFFIX linux-musl

# TODO(edenfed): Currently .NET Automatic instrumentation does not work on dotnet 6.0 with glibc,
# This is due to compilation of the .so file on a newer version of glibc than the one used by the dotnet runtime.
# The following override the .so file with our own which is compiled on the same glibc version as the dotnet runtime.
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/odigos-io/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/OpenTelemetry.AutoInstrumentation.Native-${ARCH_SUFFIX}.so && \
    case "$ARCH_SUFFIX" in \
      x64) echo "1a2964dd0ac92c29d5265ffd0093cb7d69605cdd3e98374e9cf25dbfb619855c  OpenTelemetry.AutoInstrumentation.Native-x64.so" | sha256sum -c - ;; \
      arm64) echo "06c6191aa282c1a605d919d3057b1f5ce3d1e5ada7b9804295b30bf2288976ab  OpenTelemetry.AutoInstrumentation.Native-arm64.so" | sha256sum -c - ;; \
      *) echo "no checksum pinned for arch $ARCH_SUFFIX" && exit 1 ;; \
    esac && \
    mv OpenTelemetry.AutoInstrumentation.Native-${ARCH_SUFFIX}.so linux-glibc/OpenTelemetry.AutoInstrumentation.Native.so

WORKDIR /legacy-dotnet-instrumentation
ARG LEGACY_DOTNET_OTEL_VERSION=v0.4.0-beta.1
ARG EXTENDED=false
RUN if [ "$EXTENDED" = "true" ]; then \
        wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/$LEGACY_DOTNET_OTEL_VERSION/opentelemetry-dotnet-instrumentation-linux-musl.zip && \
        echo "fb9bd397fa0a7675c239f50187eaa7e10021e9c4480fb2b67eccc1626fa1152c  opentelemetry-dotnet-instrumentation-linux-musl.zip" | sha256sum -c - && \
        unzip opentelemetry-dotnet-instrumentation-linux-musl.zip && \
        rm opentelemetry-dotnet-instrumentation-linux-musl.zip; \
    fi

# ruby-community-legacy is copied into agents-builder only when EXTENDED=true.
# Use the build target platform. This image only holds arch-specific files, and
# a cross-build's local tag is that target arch, not BUILDPLATFORM.
# Digest is pinned after the first published ruby-community-legacy release:
#   make -f agent-deps.mk upgrade-agent INSTRUMENTATION_AGENT=ruby-legacy AGENT_VERSION=vX.Y.Z
FROM public.ecr.aws/odigos/agents/ruby-community-legacy:v0.0.2@sha256:b94c04972292962b3147f835ad148bd3a4b79995f96ab68f8e43edbbb6d6c0b8 AS ruby-legacy-src

######### agents builder #########
# combine all agents into a single image
FROM --platform=$BUILDPLATFORM ${ODIGLET_BASE_IMAGE} AS agents-builder
WORKDIR /instrumentations

# java-community
ARG JAVA_OTEL_VERSION=v2.32.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# python-community/python-community3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.73-py3.8@sha256:661308cb08d293f4ff3e1823cc7ebc0537ba53dd1fff72f61b411637d62ec3d4 /instrumentations/python3.8 /instrumentations/python3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.91@sha256:ae8761929dc588b90c95178451fe90c8e57239ee8b8b3ba2c5919328b0874fda /instrumentations/python /instrumentations/python

# python-enterprise
COPY --from=public.ecr.aws/odigos/agents/python-ebpf:v0.0.14@sha256:168a709d9e586f209d2a92581bacc9786099b5ce3613bdf9e3ec8b1d6a0a317b /instrumentations/python-ebpf /instrumentations/python-ebpf

# nodejs-enterprise
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.17.1@sha256:0be4855e90ec4f2674de3124d9e9d8204153e8d0c4c57f1936e716f9a69a1ecc /instrumentations/opentelemetry-node /instrumentations/opentelemetry-node
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.17.1@sha256:0be4855e90ec4f2674de3124d9e9d8204153e8d0c4c57f1936e716f9a69a1ecc /instrumentations/nodejs-ebpf /instrumentations/nodejs-ebpf
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.17.1@sha256:0be4855e90ec4f2674de3124d9e9d8204153e8d0c4c57f1936e716f9a69a1ecc /instrumentations/nodejs-community /instrumentations/nodejs-community
# nodejs-enterprise-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.21@sha256:b9d5a9c17c66a3b2a68b169af0482871a5d499e402617b256c686dc4edcfbd13 /instrumentations/opentelemetry-node-14 /instrumentations/opentelemetry-node-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.21@sha256:b9d5a9c17c66a3b2a68b169af0482871a5d499e402617b256c686dc4edcfbd13 /instrumentations/nodejs-ebpf-14 /instrumentations/nodejs-ebpf-14

# dotnet-community
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# dotnet-legacy - Legacy .NET (only included in extended builds)
ARG EXTENDED=false
RUN --mount=type=bind,from=dotnet-builder,source=/legacy-dotnet-instrumentation,target=/legacy-dotnet-instrumentation \
    if [ "$EXTENDED" = "true" ]; then \
        cp -r /legacy-dotnet-instrumentation /instrumentations/legacy-dotnet; \
    fi

# java-ebpf-instrumentations
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v2.59.0@sha256:89c9358e9f1b8ce2a3c078052f2d4f0fe78cd31e691b6d2ebdf1defa76082c5e /instrumentations/java-ebpf /instrumentations/java-ebpf

# java-enterprise
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v2.59.0@sha256:94caf0f56db8548fe6a34b0bbb0971940d190a4601eefe647d1e875cd9d9082a /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# php-community
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.10.0@sha256:84462b470d6f552fd37d9cf2552b521b74cf76df591ed57ae4348d6318faeec6 /instrumentations/php /instrumentations/php

# ruby-community
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.11@sha256:1cb067fa84fdd24316b0c9e0fbfc057bb7055263d92fe1f31a898a8e55edcf67 /instrumentations/ruby /instrumentations/ruby

# ruby-community-legacy (only included in extended builds)
ARG EXTENDED=false
RUN --mount=type=bind,from=ruby-legacy-src,source=/instrumentations/ruby-legacy,target=/ruby-legacy \
    if [ "$EXTENDED" = "true" ]; then \
        cp -r /ruby-legacy /instrumentations/ruby-legacy; \
    fi

# loader
ARG TARGETARCH
ARG ODIGOS_LOADER_VERSION=v0.0.8
RUN wget --directory-prefix=loader https://storage.googleapis.com/odigos-loader/$ODIGOS_LOADER_VERSION/$TARGETARCH/loader.so

######### Odiglet builder #########
FROM --platform=$BUILDPLATFORM ${ODIGLET_BASE_IMAGE} AS builder

WORKDIR /go/src/github.com/odigos-io/odigos-enterprise
# Copy local modules required by the build
COPY odigosauth/ odigosauth/
COPY distros/ distros/
COPY common-enterprise/ common-enterprise/
WORKDIR ./odiglet/
# Copy rest of source code for odiglet
COPY odiglet/ .

ARG TARGETARCH
ARG LD_FLAGS
ARG RHEL=false
ENV GOINSECURE=go.opentelemetry.io*
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH LD_FLAGS="${LD_FLAGS}" make build-odiglet
# File capabilities so the binary can use eBPF privileges when run as non-root.
# Setting this caps as the permitted set (=p) so that the binary can be executed with a sub set of these capabilities
# if the environment does not require all of them. A sub-set can be configured with helm values.
RUN setcap cap_bpf,cap_perfmon,cap_sys_admin,cap_sys_ptrace,cap_dac_read_search,cap_ipc_lock,cap_sys_resource,cap_setuid,cap_setgid=p ./odiglet

# Build CSI driver binary (same binary as community odiglet; required when mountMethod is k8s-csi-driver)
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    GOOS=linux GOARCH=$TARGETARCH go build -ldflags="${LD_FLAGS}" -o odigos-csi-driver github.com/odigos-io/odigos/odiglet/cmd/csi-driver

# Build OSS deviceplugin binary (same binary as community odiglet)
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    GOOS=linux GOARCH=$TARGETARCH go build -o deviceplugin github.com/odigos-io/odigos/deviceplugin/cmd

# Download grpc_health_probe this is used by the deviceplugin container to check if the deviceplugin is healthy
ARG GRPC_HEALTH_PROBE_VERSION=v0.4.59
RUN if [ "$TARGETARCH" = "arm64" ]; then \
        GRPC_ARCH="arm64"; \
    else \
        GRPC_ARCH="amd64"; \
    fi && \
    wget -O /tmp/grpc_health_probe https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-${GRPC_ARCH} && \
    chmod +x /tmp/grpc_health_probe

# Note that we do not pass --platform=$BUILDPLATFORM here because we want to build for the target platform
FROM ${ODIGLET_BASE_IMAGE} AS rsync-base

######### agents final image #########
# Use BusyBox for the final image instead of distroless.
# Rationale:
# - Provides built-in `sh`, `cp`, and other essential Unix tools.
# - Allows `sh -c` command chaining (`cp dir1 dst1 && cp dir2 dst2`) in init containers.
# - Very small (~1MB), making it good for init containers.
FROM busybox:1.36.1 AS agents
WORKDIR /instrumentations/

# Copy all instrumentation assets from the build stage
COPY --from=agents-builder /instrumentations/ .

# make sure we run as non root user
# this is required when this container is added to a pod that has runAsNonRoot: true
USER 1000:1000

######### RHEL agents final image #########
FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS agents-rhel
ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION
LABEL "name"="Odiglet"
LABEL "vendor"="Odigos"
LABEL "maintainer"="Odigos"
LABEL "version"=$VERSION
LABEL "release"=$RELEASE
LABEL "summary"=$SUMMARY
LABEL "description"=$DESCRIPTION
WORKDIR /instrumentations/
COPY --from=agents-builder /instrumentations/ .
COPY odiglet/licenses /licenses

# make sure we run as non root user
# this is required when this container is added to a pod that has runAsNonRoot: true
USER 1000:1000

######### RHEL Odiglet #########
FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS rhel
ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION
LABEL "name"="Odiglet"
LABEL "vendor"="Odigos"
LABEL "maintainer"="Odigos"
LABEL "version"=$VERSION
LABEL "release"=$RELEASE
LABEL "summary"=$SUMMARY
LABEL "description"=$DESCRIPTION
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odigos-csi-driver /root/odigos-csi-driver
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/licenses /licenses
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/deviceplugin /root/deviceplugin
COPY --from=builder /tmp/grpc_health_probe /root/grpc_health_probe
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=agents-builder /instrumentations/ .

USER 65532:65532

CMD ["/root/odiglet"]

######### Default Odiglet #########
# Uses the distroless nossl variant: nothing here needs OpenSSL at runtime. rsync, odiglet and grpc_health_probe
# are static (odiglet-base builds rsync without OpenSSL); odigos-csi-driver and deviceplugin need only libc.
# Pinned by digest; an earlier base-debian12 digest once broke rsync.
FROM gcr.io/distroless/base-nossl-debian12@sha256:e35893f8ca1cabfbd60394af94899c20930c4615355adf97c5da660a51b61bca
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odigos-csi-driver /root/odigos-csi-driver
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/deviceplugin /root/deviceplugin
COPY --from=builder /tmp/grpc_health_probe /root/grpc_health_probe
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=agents-builder /instrumentations/ .

USER 65532:65532

CMD ["/root/odiglet"]
