ARG ODIGLET_BASE_IMAGE=registry.odigos.io/odiglet-base:v1.22

FROM python:3.11 AS python-builder
WORKDIR /python-instrumentation
# Enterprise version of ebpf-generic-instrumentation
ARG EBPF_GENERIC_VERSION="v0.0.12"
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



FROM --platform=$BUILDPLATFORM ${ODIGLET_BASE_IMAGE} AS builder

WORKDIR /go/src/github.com/odigos-io/odigos-enterprise
# Copy local modules required by the build
COPY odigosauth/ odigosauth/
COPY distros/ distros/
WORKDIR ./odiglet/
ENV GOPRIVATE=github.com/odigos-io/enterprise-go-instrumentation,github.com/odigos-io/ebpf-java-instrumentation/v2
# Pre-copy/cache go.mod for pre-downloading dependencies and only redownloading
COPY odiglet/go.mod odiglet/go.sum ./
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    go mod download
# Copy rest of source code for odiglet
COPY odiglet/ .

ARG TARGETARCH
ARG LD_FLAGS
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    GOOS=linux GOARCH=$TARGETARCH make debug-build-odiglet

# CSI driver (same as production odiglet image; required when mountMethod is k8s-csi-driver)
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    GOOS=linux GOARCH=$TARGETARCH go build -ldflags="${LD_FLAGS}" -o odigos-csi-driver github.com/odigos-io/odigos/odiglet/cmd/csi-driver

# Install delve
RUN go install github.com/go-delve/delve/cmd/dlv@latest

WORKDIR /instrumentations

# java-community
ARG JAVA_OTEL_VERSION=v2.32.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# python-community/python-community3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.73-py3.8@sha256:661308cb08d293f4ff3e1823cc7ebc0537ba53dd1fff72f61b411637d62ec3d4 /instrumentations/python3.8 /instrumentations/python3.8
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.90@sha256:26be5a681e6cde7585f0bff6714fd74dbc90515a239d4759f5b6225591a78185 /instrumentations/python /instrumentations/python

# python-enterprise
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf

# nodejs-enterprise
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.16.0@sha256:8cceebc14c511a4d7e45da44a7c9fa540b37fa5387491f7fb202ab015e45f4a5 /instrumentations/opentelemetry-node /instrumentations/opentelemetry-node
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.16.0@sha256:8cceebc14c511a4d7e45da44a7c9fa540b37fa5387491f7fb202ab015e45f4a5 /instrumentations/nodejs-ebpf /instrumentations/nodejs-ebpf
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.16.0@sha256:8cceebc14c511a4d7e45da44a7c9fa540b37fa5387491f7fb202ab015e45f4a5 /instrumentations/nodejs-community /instrumentations/nodejs-community
# nodejs-enterprise-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.21@sha256:b9d5a9c17c66a3b2a68b169af0482871a5d499e402617b256c686dc4edcfbd13 /instrumentations/opentelemetry-node-14 /instrumentations/opentelemetry-node-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.21@sha256:b9d5a9c17c66a3b2a68b169af0482871a5d499e402617b256c686dc4edcfbd13 /instrumentations/nodejs-ebpf-14 /instrumentations/nodejs-ebpf-14

# dotnet-community
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# java-ebpf-instrumentations
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v2.59.0@sha256:89c9358e9f1b8ce2a3c078052f2d4f0fe78cd31e691b6d2ebdf1defa76082c5e /instrumentations/java-ebpf /instrumentations/java-ebpf
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v2.59.0@sha256:94caf0f56db8548fe6a34b0bbb0971940d190a4601eefe647d1e875cd9d9082a /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# php-community
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.9.0@sha256:dd1469ad9d8551a647dcc072bb2f8adb683e0484124e261ad0972ed24eef50c3 /instrumentations/php /instrumentations/php

# ruby-community
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.9@sha256:5177737aba83e507ee968223fbd821d76e2df4e74bd1711e2d49e8d59f671d8c /instrumentations/ruby /instrumentations/ruby

# ruby-community-legacy
# Digest is pinned after the first published ruby-community-legacy release:
#   make -f agent-deps.mk upgrade-agent INSTRUMENTATION_AGENT=ruby-legacy AGENT_VERSION=vX.Y.Z
COPY --from=public.ecr.aws/odigos/agents/ruby-community-legacy:v0.0.2@sha256:b94c04972292962b3147f835ad148bd3a4b79995f96ab68f8e43edbbb6d6c0b8 /instrumentations/ruby-legacy /instrumentations/ruby-legacy

# loader
ARG ODIGOS_LOADER_VERSION=v0.0.8
RUN wget --directory-prefix=loader https://storage.googleapis.com/odigos-loader/$ODIGOS_LOADER_VERSION/$TARGETARCH/loader.so

# Note that we do not pass --platform=$BUILDPLATFORM here because we want to build for the target platform
FROM ${ODIGLET_BASE_IMAGE} AS rsync-base

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odigos-csi-driver /root/odigos-csi-driver
COPY --from=builder /go/bin/dlv /root/dlv
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .

EXPOSE 2345
ENTRYPOINT ["/root/dlv" ,"--listen=:2345", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/root/odiglet"]
