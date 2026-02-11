ARG ODIGLET_BASE_IMAGE=registry.odigos.io/odiglet-base:v1.10

######### python builder #########
FROM python:3.11.9 AS python-builder
ARG ODIGOS_VERSION
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install -r requirements.txt --target workspace
RUN echo "VERSION = \"$ODIGOS_VERSION\";" > /python-instrumentation/workspace/initializer/version.py

# Enterprise version of ebpf-generic-instrumentation
ARG EBPF_GENERIC_VERSION="v0.0.7"
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone -b ${EBPF_GENERIC_VERSION} https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-generic-instrumentation.git \
    && cd ebpf-generic-instrumentation
RUN mkdir enterprise-workspace && pip install --target enterprise-workspace ./ebpf-generic-instrumentation/dtrace-injector

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

# Java
ARG JAVA_OTEL_VERSION=v2.6.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# Python
COPY --from=python-builder /python-instrumentation/workspace /instrumentations/python

# Python eBPF
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf

# nodejs-enterprise
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.0.14 /instrumentations/opentelemetry-node /instrumentations/opentelemetry-node
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.0.14 /instrumentations/nodejs-ebpf /instrumentations/nodejs-ebpf

# .NET
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# Legacy .NET (only included in extended builds)
ARG EXTENDED=false
RUN --mount=type=bind,from=dotnet-builder,source=/legacy-dotnet-instrumentation,target=/legacy-dotnet-instrumentation \
    if [ "$EXTENDED" = "true" ]; then \
        cp -r /legacy-dotnet-instrumentation /instrumentations/legacy-dotnet; \
    fi

# Java
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v1.13.16 /instrumentations/java-ebpf /instrumentations/java-ebpf
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v1.13.16 /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# PHP
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.2.5 /instrumentations/php /instrumentations/php

# Ruby
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.7 /instrumentations/ruby /instrumentations/ruby

# loader
ARG TARGETARCH
ARG ODIGOS_LOADER_VERSION=v0.0.6
RUN wget --directory-prefix=loader https://storage.googleapis.com/odigos-loader/$ODIGOS_LOADER_VERSION/$TARGETARCH/loader.so

######### Odiglet builder #########
FROM --platform=$BUILDPLATFORM ${ODIGLET_BASE_IMAGE} AS builder

RUN --mount=type=secret,id=github_token \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"

WORKDIR /go/src/github.com/odigos-io/odigos-enterprise
# Copyy local modules required by the build
COPY odigosauth/ odigosauth/
COPY distros/ distros/
WORKDIR ./odiglet/
# Copy rest of source code for odiglet
COPY odiglet/ .

ARG TARGETARCH
ARG LD_FLAGS
ARG RHEL=false
ENV GOINSECURE=go.opentelemetry.io*
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    GOOS=linux GOARCH=$TARGETARCH LD_FLAGS="${LD_FLAGS}" make build-odiglet

# Build deviceplugin binary
WORKDIR /go/src/github.com/odigos-io/odigos-enterprise/deviceplugin
COPY deviceplugin/ .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    GOOS=linux GOARCH=$TARGETARCH go build -o deviceplugin ./cmd

# Download grpc_health_probe this is used by the deviceplugin container to check if the deviceplugin is healthy
ARG GRPC_HEALTH_PROBE_VERSION=v0.4.44
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
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/licenses /licenses
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/deviceplugin/deviceplugin /root/deviceplugin
COPY --from=builder /tmp/grpc_health_probe /root/grpc_health_probe
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=agents-builder /instrumentations/ .
CMD ["/root/odiglet"]

######### Default Odiglet #########
# pinning the distroless base image to a specific commit since the hash after it broke rsync somehow.
FROM gcr.io/distroless/base:3443777deec6bd5c58be682f286aefffd1c871ae
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/deviceplugin/deviceplugin /root/deviceplugin
COPY --from=builder /tmp/grpc_health_probe /root/grpc_health_probe
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=agents-builder /instrumentations/ .
CMD ["/root/odiglet"]
