ARG ODIGLET_BASE_IMAGE=registry.odigos.io/odiglet-base:v1.17

######### dotnet builder #########
FROM --platform=$BUILDPLATFORM busybox:1.36.1 AS dotnet-builder
WORKDIR /dotnet-instrumentation
ARG DOTNET_OTEL_VERSION=v1.9.0
ARG TARGETARCH
RUN if [ "$TARGETARCH" = "arm64" ]; then \
    echo "arm64" > /tmp/arch_suffix; \
    else \
    echo "x64" > /tmp/arch_suffix; \
    fi
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    unzip opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    rm opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    mv linux-$ARCH_SUFFIX linux-glibc
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    unzip -o opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    rm opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    mv linux-musl-$ARCH_SUFFIX linux-musl

# TODO(edenfed): Currently .NET Automatic instrumentation does not work on dotnet 6.0 with glibc,
# This is due to compilation of the .so file on a newer version of glibc than the one used by the dotnet runtime.
# The following override the .so file with our own which is compiled on the same glibc version as the dotnet runtime.
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/odigos-io/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/OpenTelemetry.AutoInstrumentation.Native-${ARCH_SUFFIX}.so && \
    mv OpenTelemetry.AutoInstrumentation.Native-${ARCH_SUFFIX}.so linux-glibc/OpenTelemetry.AutoInstrumentation.Native.so

WORKDIR /legacy-dotnet-instrumentation
ARG LEGACY_DOTNET_OTEL_VERSION=v0.4.0-beta.1
ARG EXTENDED=false
RUN if [ "$EXTENDED" = "true" ]; then \
        wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/$LEGACY_DOTNET_OTEL_VERSION/opentelemetry-dotnet-instrumentation-linux-musl.zip && \
        unzip opentelemetry-dotnet-instrumentation-linux-musl.zip && \
        rm opentelemetry-dotnet-instrumentation-linux-musl.zip; \
    fi

######### agents builder #########
# combine all agents into a single image
FROM --platform=$BUILDPLATFORM ${ODIGLET_BASE_IMAGE} AS agents-builder
WORKDIR /instrumentations

# java-community
ARG JAVA_OTEL_VERSION=v2.6.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# python-community/python-community3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.73-py3.8@sha256:661308cb08d293f4ff3e1823cc7ebc0537ba53dd1fff72f61b411637d62ec3d4 /instrumentations/python3.8 /instrumentations/python3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.90@sha256:26be5a681e6cde7585f0bff6714fd74dbc90515a239d4759f5b6225591a78185 /instrumentations/python /instrumentations/python

# python-enterprise
COPY --from=public.ecr.aws/odigos/agents/python-ebpf:v0.0.14@sha256:168a709d9e586f209d2a92581bacc9786099b5ce3613bdf9e3ec8b1d6a0a317b /instrumentations/python-ebpf /instrumentations/python-ebpf

# nodejs-enterprise
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.13.0@sha256:420770b2f7e8863e79a0f03c339083806e1ef79ae316955240686f547ec9d781 /instrumentations/opentelemetry-node /instrumentations/opentelemetry-node
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.13.0@sha256:420770b2f7e8863e79a0f03c339083806e1ef79ae316955240686f547ec9d781 /instrumentations/nodejs-ebpf /instrumentations/nodejs-ebpf
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.13.0@sha256:420770b2f7e8863e79a0f03c339083806e1ef79ae316955240686f547ec9d781 /instrumentations/nodejs-community /instrumentations/nodejs-community
# nodejs-enterprise-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.20@sha256:39e18324ddfac277b557114be36c062bc95bfd5daba38fde91fce1a2b6543418 /instrumentations/opentelemetry-node-14 /instrumentations/opentelemetry-node-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.20@sha256:39e18324ddfac277b557114be36c062bc95bfd5daba38fde91fce1a2b6543418 /instrumentations/nodejs-ebpf-14 /instrumentations/nodejs-ebpf-14

# dotnet-community
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# dotnet-legacy - Legacy .NET (only included in extended builds)
ARG EXTENDED=false
RUN --mount=type=bind,from=dotnet-builder,source=/legacy-dotnet-instrumentation,target=/legacy-dotnet-instrumentation \
    if [ "$EXTENDED" = "true" ]; then \
        cp -r /legacy-dotnet-instrumentation /instrumentations/legacy-dotnet; \
    fi

# java-ebpf-instrumentations
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v2.41.1@sha256:deb25264d273c0a41a967d4edd1d32bc48cd5656aa486757aa4f129b0689b739 /instrumentations/java-ebpf /instrumentations/java-ebpf

# java-enterprise
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v2.41.1@sha256:0dd655736137035e40c595df08b3479bc4e3ac8bb8e37eee3245da7d676d63d9 /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# php-community
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.4.1@sha256:feab1097b5adbfbb2cf89dadc7d16e3debbae2c8b6138d41fedb214d6b9b04d8 /instrumentations/php /instrumentations/php

# ruby-community
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.8@sha256:a9df41f7684f550a3b7e1693456f7c4eb4ce432aba82d4b9e22d2a73687473b1 /instrumentations/ruby /instrumentations/ruby

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
COPY ebpfshare/ ebpfshare/
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
    GOOS=linux GOARCH=$TARGETARCH LD_FLAGS="${LD_FLAGS}" make build-odiglet

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
ARG GRPC_HEALTH_PROBE_VERSION=v0.4.52
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
CMD ["/root/odiglet"]

######### Default Odiglet #########
# pinning the distroless base image to a specific commit since the hash after it broke rsync somehow.
FROM gcr.io/distroless/base-debian12@sha256:e7e678c88c59e70e105a46549bb3fbfb3d732ee3b4afd3a19fdab2e15afaa6b3
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odigos-csi-driver /root/odigos-csi-driver
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/deviceplugin /root/deviceplugin
COPY --from=builder /tmp/grpc_health_probe /root/grpc_health_probe
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=agents-builder /instrumentations/ .
CMD ["/root/odiglet"]
