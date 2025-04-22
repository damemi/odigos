FROM python:3.11.9 AS python-builder
ARG ODIGOS_VERSION
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install -r requirements.txt --target workspace
RUN echo "VERSION = \"$ODIGOS_VERSION\";" > /python-instrumentation/workspace/initializer/version.py

# Enterprise version of ebpf-generic-instrumentation
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-generic-instrumentation.git
RUN mkdir enterprise-workspace && pip install --target enterprise-workspace ./ebpf-generic-instrumentation/dtrace-injector

FROM node:21.6.2-bullseye AS nodejs-builder
# NodeJS eBPF
WORKDIR /nodejs-instrumentation
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-nodejs-instrumentation.git \
    && cd ebpf-nodejs-instrumentation/dtrace-injector \
    && make build

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
    mv linux-$ARCH_SUFFIX linux-glibc-$ARCH_SUFFIX && \
    wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip && \
    unzip opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip "linux-musl-$ARCH_SUFFIX/*" -d . && \
    rm opentelemetry-dotnet-instrumentation-linux-musl-${ARCH_SUFFIX}.zip

# TODO(edenfed): Currently .NET Automatic instrumentation does not work on dotnet 6.0 with glibc,
# This is due to compilation of the .so file on a newer version of glibc than the one used by the dotnet runtime.
# The following override the .so file with our own which is compiled on the same glibc version as the dotnet runtime.
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/odigos-io/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/OpenTelemetry.AutoInstrumentation.Native-${ARCH_SUFFIX}.so && \
    mv OpenTelemetry.AutoInstrumentation.Native-${ARCH_SUFFIX}.so linux-glibc-${ARCH_SUFFIX}/OpenTelemetry.AutoInstrumentation.Native.so

WORKDIR /legacy-dotnet-instrumentation
ARG LEGACY_DOTNET_OTEL_VERSION=v0.4.0-beta.1
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/$LEGACY_DOTNET_OTEL_VERSION/opentelemetry-dotnet-instrumentation-linux-musl.zip .
RUN unzip opentelemetry-dotnet-instrumentation-linux-musl.zip && rm opentelemetry-dotnet-instrumentation-linux-musl.zip

# Dtrace injector
FROM keyval/javabuilder-base:v3.0 AS java-builder
WORKDIR /dtrace-injector
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-java-instrumentation.git
WORKDIR /dtrace-injector/ebpf-java-instrumentation/utils
RUN mvn clean package -DskipTests
RUN mkdir -p ../dtrace-injector/target && cp ./target/utils.jar ../dtrace-injector/target/
WORKDIR /dtrace-injector/ebpf-java-instrumentation/dtrace-injector
ENV JAVA_HOME=/usr/local/openjdk-11
RUN make generate-headers
RUN make build-jni && mvn package -DskipTests
# build extension
WORKDIR /dtrace-injector/ebpf-java-instrumentation/otel_agent_extension
RUN make gradle-build
RUN make build-jni


# PHP: Clone agents repo (contains pre-compiled binaries and libraries for each PHP version)
FROM --platform=$BUILDPLATFORM php:8-cli AS php-agents
WORKDIR /php-agents
ARG PHP_AGENT_VERSION=v0.0.2
RUN apt-get update && apt-get upgrade -y && apt-get install -y git
RUN git clone https://github.com/odigos-io/opentelemetry-php \
    && cd opentelemetry-php \
    && git checkout tags/${PHP_AGENT_VERSION}


FROM --platform=$BUILDPLATFORM keyval/odiglet-base:v1.7 AS builder

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
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    GOOS=linux GOARCH=$TARGETARCH make build-odiglet

WORKDIR /instrumentations

# Java
ARG JAVA_OTEL_VERSION=v2.6.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# Python
COPY --from=python-builder /python-instrumentation/workspace /instrumentations/python

# Python eBPF
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf

# NodeJS eBPF
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/dist /instrumentations/nodejs-ebpf/
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/lib /instrumentations/nodejs-ebpf/lib
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/build/Release/ /instrumentations/nodejs-ebpf/build/Release/
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/node_modules /instrumentations/nodejs-ebpf/node_modules

# .NET
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet
COPY --from=dotnet-builder /legacy-dotnet-instrumentation /instrumentations/legacy-dotnet

# Java eBPF
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/tracing_probes.so /instrumentations/java-ebpf/tracing_probes.so
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/dtrace-injector-1.0-SNAPSHOT.jar /instrumentations/java-ebpf/dtrace-injector.jar
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/utils.jar /instrumentations/java-ebpf/utils.jar

# Java Agent Extension
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/otel_agent_extension/extension/build/libs/odigos-opentelemetry-java-extension-1.0.jar /instrumentations/java-ext-ebpf/otel_agent_extension.jar
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/otel_agent_extension/extension/end_span_usdt.so /instrumentations/java-ext-ebpf/end_span_usdt.so
RUN cp /instrumentations/java/javaagent.jar /instrumentations/java-ext-ebpf/javaagent.jar

# PHP
COPY --from=php-agents /php-agents/opentelemetry-php/8.0 /instrumentations/php/8.0
COPY --from=php-agents /php-agents/opentelemetry-php/8.1 /instrumentations/php/8.1
COPY --from=php-agents /php-agents/opentelemetry-php/8.2 /instrumentations/php/8.2
COPY --from=php-agents /php-agents/opentelemetry-php/8.3 /instrumentations/php/8.3
COPY --from=php-agents /php-agents/opentelemetry-php/8.4 /instrumentations/php/8.4

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .
CMD ["/root/odiglet"]
