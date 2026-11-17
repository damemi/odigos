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

WORKDIR /go/src/github.com/odigos-io/odigos
ENV GOPRIVATE=github.com/odigos-io/enterprise-go-instrumentation,github.com/odigos-io/ebpf-java-instrumentation/v2,github.com/odigos-io/odigos-device-plugin
# odiglet/enterprise replaces these odigos modules with local paths.
COPY api/ api/
COPY common/ common/
COPY deviceplugin/ deviceplugin/
COPY distros/ distros/
COPY instrumentation/ instrumentation/
COPY k8sutils/ k8sutils/
COPY odigosauth/ odigosauth/
COPY opampserver/ opampserver/
COPY procdiscovery/ procdiscovery/
COPY odiglet/ odiglet/
WORKDIR /go/src/github.com/odigos-io/odigos/odiglet/enterprise
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    go mod download

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
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.91@sha256:ae8761929dc588b90c95178451fe90c8e57239ee8b8b3ba2c5919328b0874fda /instrumentations/python /instrumentations/python

# python-enterprise
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf

# nodejs-enterprise
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.21.0@sha256:20ffcdbfa68789b0edc9f8543c9556011087ad9a496040ee78724f3f346e55f2 /instrumentations/opentelemetry-node /instrumentations/opentelemetry-node
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.21.0@sha256:20ffcdbfa68789b0edc9f8543c9556011087ad9a496040ee78724f3f346e55f2 /instrumentations/nodejs-ebpf /instrumentations/nodejs-ebpf
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise:v0.21.0@sha256:20ffcdbfa68789b0edc9f8543c9556011087ad9a496040ee78724f3f346e55f2 /instrumentations/nodejs-community /instrumentations/nodejs-community
# nodejs-enterprise-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.21@sha256:b9d5a9c17c66a3b2a68b169af0482871a5d499e402617b256c686dc4edcfbd13 /instrumentations/opentelemetry-node-14 /instrumentations/opentelemetry-node-14
COPY --from=public.ecr.aws/odigos/agents/nodejs-enterprise-14:v0.0.21@sha256:b9d5a9c17c66a3b2a68b169af0482871a5d499e402617b256c686dc4edcfbd13 /instrumentations/nodejs-ebpf-14 /instrumentations/nodejs-ebpf-14

# dotnet-community
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# java-ebpf-instrumentations
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v2.62.0@sha256:bb8fe42359a144bce741187277041dbd94183dec6a69ab398315ca98501dfb3c /instrumentations/java-ebpf /instrumentations/java-ebpf
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v2.62.0@sha256:666a455f9408f03d10911ec33725a873782098a3c2fb3ac2bca4529eb9f1cd51 /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# php-community
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.12.0@sha256:29b949cebd81bf9e8d5d715bfb72e6b45f9e2151f8e7fb8419c520faae2af16c /instrumentations/php /instrumentations/php

# ruby-community
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.11@sha256:1cb067fa84fdd24316b0c9e0fbfc057bb7055263d92fe1f31a898a8e55edcf67 /instrumentations/ruby /instrumentations/ruby

# ruby-community-legacy
# Digest is pinned after the first published ruby-community-legacy release:
#   make -f agent-deps.mk upgrade-agent INSTRUMENTATION_AGENT=ruby-legacy AGENT_VERSION=vX.Y.Z
COPY --from=public.ecr.aws/odigos/agents/ruby-community-legacy:v0.0.3@sha256:2922c58f2f12a00e6415883748fefaf5f42066a3d57093d06287d8f954d352b6 /instrumentations/ruby-legacy /instrumentations/ruby-legacy

# loader
ARG ODIGOS_LOADER_VERSION=v0.0.8
RUN wget --directory-prefix=loader https://storage.googleapis.com/odigos-loader/$ODIGOS_LOADER_VERSION/$TARGETARCH/loader.so

# Note that we do not pass --platform=$BUILDPLATFORM here because we want to build for the target platform
FROM ${ODIGLET_BASE_IMAGE} AS rsync-base

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/odigos-io/odigos/odiglet/enterprise/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos/odiglet/enterprise/odigos-csi-driver /root/odigos-csi-driver
COPY --from=builder /go/bin/dlv /root/dlv
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-base /usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .

EXPOSE 2345
ENTRYPOINT ["/root/dlv" ,"--listen=:2345", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/root/odiglet"]
