FROM python:3.11.9 AS python-builder
ARG ODIGOS_VERSION
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install -r requirements.txt --target workspace
RUN echo "VERSION = \"$ODIGOS_VERSION\";" > /python-instrumentation/workspace/initializer/version.py

# Enterprise version of ebpf-generic-instrumentation
ARG EBPF_GENERIC_VERSION="v0.0.5"
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone -b ${EBPF_GENERIC_VERSION} https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-generic-instrumentation.git \
    && cd ebpf-generic-instrumentation
RUN mkdir enterprise-workspace && pip install --target enterprise-workspace ./ebpf-generic-instrumentation/dtrace-injector


##################################################
################## Node.js Base Agent ############
##################################################

# The Node.js agent is built in multiple stages so it can be built with either upstream
# @odigos/opentelemetry-node or with a local clone to test changes during development.
# The implemntation is based on the following blog post:
# https://www.docker.com/blog/dockerfiles-now-support-multiple-build-contexts/

# The first build stage 'nodejs-agent-clone' clones the agent sources from github main branch.
FROM alpine AS nodejs-agent-clone
RUN apk add git
WORKDIR /src
ARG NODEJS_AGENT_VERSION=main
RUN git clone -b $NODEJS_AGENT_VERSION https://github.com/odigos-io/opentelemetry-node.git

# The second build stage 'nodejs-agent-src' prepares the actual code we are going to compile and embed in odiglet.
# By default, it uses the previous 'nodejs-agent-src' stage, but one can override it by setting the
# --build-context nodejs-agent-src=../opentelemetry-node flag in the docker build command.
# This allows us to nobe the agent sources and test changes during development.
# The output of this stage is the resolved source code to be used in the next stage.
FROM scratch AS nodejs-agent-src
COPY --from=nodejs-agent-clone /src/opentelemetry-node /

# The third step 'nodejs-agent-build' compiles the agent sources and prepares it for
# Run yarn install to generate the production node_modules directory
FROM node:18 AS nodejs-agent-build
# Run yarn install to generate the production node_modules directory
WORKDIR /opentelemetry-node-prod
COPY --from=nodejs-agent-src package.json yarn.lock ./
RUN yarn install --frozen-lockfile --production
# Build the agent from typescript sources
ARG ODIGOS_VERSION
WORKDIR /opentelemetry-node
COPY --from=nodejs-agent-src package.json yarn.lock ./
RUN yarn --frozen-lockfile
COPY --from=nodejs-agent-src / .
RUN echo "export const VERSION = \"$ODIGOS_VERSION\";" > ./src/version.ts
RUN yarn compile


##################################################
############# nodejs-enterprise Agent ############
##################################################

FROM alpine AS nodejs-enterprise-agent-clone
RUN apk add git
WORKDIR /src
ARG NODEJS_ENTERPRISE_AGENT_VERSION=main
RUN --mount=type=secret,id=github_token git clone -b $NODEJS_ENTERPRISE_AGENT_VERSION https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-nodejs-instrumentation.git

FROM scratch AS nodejs-enterprise-agent-src
COPY --from=nodejs-enterprise-agent-clone /src/ebpf-nodejs-instrumentation /

FROM node:21.6.2-bookworm AS nodejs-enterprise-agent-build
RUN apt update -y && apt install -y systemtap-sdt-dev
WORKDIR /nodejs-enterprise
COPY --from=nodejs-enterprise-agent-src / .
WORKDIR /nodejs-enterprise/dtrace-injector
RUN make build
# Create production node_modules without dev dependencies to make the image smaller
WORKDIR /nodejs-enterprise-prod
COPY --from=nodejs-enterprise-agent-src / .
WORKDIR /nodejs-enterprise-prod/dtrace-injector
RUN yarn install --frozen-lockfile --production



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
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/$LEGACY_DOTNET_OTEL_VERSION/opentelemetry-dotnet-instrumentation-linux-musl.zip .
RUN unzip opentelemetry-dotnet-instrumentation-linux-musl.zip && rm opentelemetry-dotnet-instrumentation-linux-musl.zip

# PHP
FROM --platform=$BUILDPLATFORM maniator/gh AS php-agents
WORKDIR /php-agents
ARG TARGETARCH
ARG PHP_AGENT_VERSION="v0.1.21"
ARG PHP_VERSIONS="8.0 8.1 8.2 8.3 8.4"
ENV PHP_VERSIONS=${PHP_VERSIONS}
# Clone agents repo (contains pre-compiled binaries, and pre-installed dependencies for each PHP version)
RUN git clone https://github.com/odigos-io/opentelemetry-php \
    && cd opentelemetry-php \
    && git checkout tags/${PHP_AGENT_VERSION}
# Move the pre-compiled binaries to the correct directories
RUN for v in ${PHP_VERSIONS}; do \
    mv opentelemetry-php/$v/bin/${TARGETARCH}/* opentelemetry-php/$v/; \
    rm -rf opentelemetry-php/$v/bin; \
    done


# Ruby
FROM --platform=$BUILDPLATFORM maniator/gh AS ruby-agents
WORKDIR /ruby-agents
ARG TARGETARCH
ARG RUBY_AGENT_VERSION="v0.0.5"
ARG RUBY_VERSIONS="3.1 3.2 3.3 3.4"
ENV RUBY_VERSIONS=${RUBY_VERSIONS}
# Clone agents repo (contains pre-compiled binaries, and pre-installed dependencies for each Ruby version)
RUN git clone https://github.com/odigos-io/opentelemetry-ruby \
    && cd opentelemetry-ruby \
    && git checkout tags/${RUBY_AGENT_VERSION}
# Move the gems & binaries to the correct directories
RUN for v in ${RUBY_VERSIONS}; do \
    mv opentelemetry-ruby/$v/${TARGETARCH}/* opentelemetry-ruby/$v/; \
    cp opentelemetry-ruby/Gemfile opentelemetry-ruby/$v/Gemfile; \
    cp opentelemetry-ruby/index.rb opentelemetry-ruby/$v/index.rb; \
    rm -rf opentelemetry-ruby/$v/amd64; \
    rm -rf opentelemetry-ruby/$v/arm64; \
    done

# Compile rsync statically for distroless image
# don't specify the platform here, since we want to compile for multi architecture natively with gcc
FROM registry.odigos.io/odiglet-base:v1.8 AS rsync-builder
ARG RSYNC_VERSION=3.2.7
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    wget \
    ca-certificates \
    libacl1-dev \
    libattr1-dev \
    libpopt-dev \
    liblz4-dev \
    libzstd-dev \
    libxxhash-dev \
    libssl-dev \
    && rm -rf /var/lib/apt/lists/*

RUN wget https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz \
    && tar -xzf rsync-${RSYNC_VERSION}.tar.gz \
    && cd rsync-${RSYNC_VERSION} \
    && ./configure --prefix=/usr LDFLAGS="-static" \
    && make \
    && make install DESTDIR=/rsync-install \
    && cd .. \
    && rm -rf rsync-${RSYNC_VERSION}*

FROM --platform=$BUILDPLATFORM registry.odigos.io/odiglet-base:v1.8 AS builder

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
ARG GRPC_HEALTH_PROBE_VERSION=v0.4.24
RUN if [ "$TARGETARCH" = "arm64" ]; then \
        GRPC_ARCH="arm64"; \
    else \
        GRPC_ARCH="amd64"; \
    fi && \
    wget -O /tmp/grpc_health_probe https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-${GRPC_ARCH} && \
    chmod +x /tmp/grpc_health_probe

WORKDIR /instrumentations

# Java
ARG JAVA_OTEL_VERSION=v2.6.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# Python
COPY --from=python-builder /python-instrumentation/workspace /instrumentations/python

# Python eBPF
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf


# nodejs base agent
COPY --from=nodejs-agent-build /opentelemetry-node/package.json /instrumentations/opentelemetry-node/package.json
COPY --from=nodejs-agent-build /opentelemetry-node/LICENSE /instrumentations/opentelemetry-node/LICENSE
COPY --from=nodejs-agent-build /opentelemetry-node/build /instrumentations/opentelemetry-node/build
COPY --from=nodejs-agent-build /opentelemetry-node-prod/node_modules /instrumentations/opentelemetry-node/node_modules

# nodejs-enterprise ebpf agent
COPY --from=nodejs-enterprise-agent-build /nodejs-enterprise/dtrace-injector/dist /instrumentations/nodejs-ebpf/
COPY --from=nodejs-enterprise-agent-build /nodejs-enterprise/dtrace-injector/lib /instrumentations/nodejs-ebpf/lib
COPY --from=nodejs-enterprise-agent-build /nodejs-enterprise/dtrace-injector/build/Release/ /instrumentations/nodejs-ebpf/build/Release/
COPY --from=nodejs-enterprise-agent-build /nodejs-enterprise-prod/dtrace-injector/node_modules /instrumentations/nodejs-ebpf/node_modules

# .NET
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet
COPY --from=dotnet-builder /legacy-dotnet-instrumentation /instrumentations/legacy-dotnet

# Java
COPY --from=public.ecr.aws/odigos/agents/java-ebpf-instrumentations:v0.5.0 /instrumentations/java-ebpf /instrumentations/java-ebpf
COPY --from=public.ecr.aws/odigos/agents/java-enterprise:v0.5.0 /instrumentations/java-ext-ebpf /instrumentations/java-ext-ebpf

# PHP
COPY --from=php-agents /php-agents/opentelemetry-php/8.0 /instrumentations/php/8.0
COPY --from=php-agents /php-agents/opentelemetry-php/8.1 /instrumentations/php/8.1
COPY --from=php-agents /php-agents/opentelemetry-php/8.2 /instrumentations/php/8.2
COPY --from=php-agents /php-agents/opentelemetry-php/8.3 /instrumentations/php/8.3
COPY --from=php-agents /php-agents/opentelemetry-php/8.4 /instrumentations/php/8.4

# Ruby
COPY --from=ruby-agents /ruby-agents/opentelemetry-ruby/3.1 /instrumentations/ruby/3.1
COPY --from=ruby-agents /ruby-agents/opentelemetry-ruby/3.2 /instrumentations/ruby/3.2
COPY --from=ruby-agents /ruby-agents/opentelemetry-ruby/3.3 /instrumentations/ruby/3.3
COPY --from=ruby-agents /ruby-agents/opentelemetry-ruby/3.4 /instrumentations/ruby/3.4

# loader
ARG ODIGOS_LOADER_VERSION=v0.0.5
RUN wget --directory-prefix=loader https://storage.googleapis.com/odigos-loader/$ODIGOS_LOADER_VERSION/$TARGETARCH/loader.so

FROM gcr.io/distroless/base:latest
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/deviceplugin/deviceplugin /root/deviceplugin
COPY --from=builder /tmp/grpc_health_probe /root/grpc_health_probe
# Copy statically compiled rsync (no shared libraries needed)
COPY --from=rsync-builder /rsync-install/usr/bin/rsync /usr/bin/rsync
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .
CMD ["/root/odiglet"]
