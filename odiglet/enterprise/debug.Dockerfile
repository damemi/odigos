ARG ODIGLET_BASE_IMAGE=registry.odigos.io/odiglet-base:v1.16

FROM python:3.11 AS python-builder
WORKDIR /python-instrumentation
# Enterprise version of ebpf-generic-instrumentation
ARG EBPF_GENERIC_VERSION="v0.0.9"
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone -b ${EBPF_GENERIC_VERSION} https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-generic-instrumentation.git \
    && cd ebpf-generic-instrumentation \
    && git checkout tags/${EBPF_GENERIC_VERSION}
RUN mkdir enterprise-workspace && pip install --target enterprise-workspace ./ebpf-generic-instrumentation/dtrace-injector

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



FROM --platform=$BUILDPLATFORM ${ODIGLET_BASE_IMAGE} AS builder

RUN --mount=type=secret,id=github_token \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"

WORKDIR /go/src/github.com/odigos-io/odigos-enterprise
# Copy local modules required by the build
COPY odigosauth/ odigosauth/
COPY distros/ distros/
WORKDIR ./odiglet/
ENV GOPRIVATE=github.com/odigos-io/enterprise-go-instrumentation,github.com/odigos-io/ebpf-java-instrumentation/v2
# Pre-copy/cache go.mod for pre-downloading dependencies and only redownloading
COPY odiglet/go.mod odiglet/go.sum ./
RUN --mount=type=cache,target=/go/pkg \
    go mod download
# Copy rest of source code for odiglet
COPY odiglet/ .

ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    GOOS=linux GOARCH=$TARGETARCH make debug-build-odiglet

# Install delve
RUN go install github.com/go-delve/delve/cmd/dlv@latest

WORKDIR /instrumentations

# java-community
ARG JAVA_OTEL_VERSION=v1.29.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# python-community/python-community3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.72-py3.8 /instrumentations/python3.8 /instrumentations/python3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.78 /instrumentations/python /instrumentations/python

# python-enterprise
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf

# nodejs-enterprise
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.3.0 /instrumentations/opentelemetry-node /instrumentations/opentelemetry-node
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.3.0 /instrumentations/nodejs-ebpf /instrumentations/nodejs-ebpf
# nodejs-enterprise-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.18 /instrumentations/opentelemetry-node-14 /instrumentations/opentelemetry-node-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.18 /instrumentations/nodejs-ebpf-14 /instrumentations/nodejs-ebpf-14

# dotnet-community
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# java-ebpf-instrumentations
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v2.4.1 /instrumentations/java-ebpf /instrumentations/java-ebpf
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v2.4.1 /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# php-community
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.3.0 /instrumentations/php /instrumentations/php

# ruby-community
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.8 /instrumentations/ruby /instrumentations/ruby

# loader
ARG ODIGOS_LOADER_VERSION=v0.0.8
RUN wget --directory-prefix=loader https://storage.googleapis.com/odigos-loader/$ODIGOS_LOADER_VERSION/$TARGETARCH/loader.so

# Note that we do not pass --platform=$BUILDPLATFORM here because we want to build for the target platform
FROM ${ODIGLET_BASE_IMAGE} AS rsync-base

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/bin/dlv /root/dlv
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .

EXPOSE 2345
ENTRYPOINT ["/root/dlv" ,"--listen=:2345", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/root/odiglet"]

