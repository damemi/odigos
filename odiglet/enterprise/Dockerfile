FROM python:3.11 AS python-builder
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install -r requirements.txt --target workspace

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
ARG DOTNET_OTEL_VERSION=v1.7.0
ARG TARGETARCH
RUN if [ "$TARGETARCH" = "arm64" ]; then \
        echo "arm64" > /tmp/arch_suffix; \
    else \
        echo "x64" > /tmp/arch_suffix; \
    fi

# TODO(edenfed): Support both glibc and musl.
RUN ARCH_SUFFIX=$(cat /tmp/arch_suffix) && \
    wget https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    unzip opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip && \
    rm opentelemetry-dotnet-instrumentation-linux-glibc-${ARCH_SUFFIX}.zip

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

FROM --platform=$BUILDPLATFORM keyval/odiglet-base:v1.5 AS builder

RUN --mount=type=secret,id=github_token \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"

WORKDIR /go/src/github.com/odigos-io/odigos-enterprise
# Copyy local modules required by the build
COPY odigosauth/ odigosauth/
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

# Java eBPF
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/tracing_probes.so /instrumentations/java-ebpf/tracing_probes.so
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/dtrace-injector-1.0-SNAPSHOT-full.jar /instrumentations/java-ebpf/dtrace-injector.jar
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/utils.jar /instrumentations/java-ebpf/utils.jar

# Java Agent Extension
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/otel_agent_extension/extension/build/libs/odigos-opentelemetry-java-extension-1.0.jar /instrumentations/java-ext-ebpf/otel_agent_extension.jar
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/otel_agent_extension/extension/end_span_usdt.so /instrumentations/java-ext-ebpf/end_span_usdt.so
RUN cp /instrumentations/java/javaagent.jar /instrumentations/java-ext-ebpf/javaagent.jar

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .
CMD ["/root/odiglet"]
