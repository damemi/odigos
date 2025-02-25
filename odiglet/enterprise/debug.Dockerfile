FROM python:3.11 AS python-builder
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install --target workspace -r requirements.txt

# Enterprise version of ebpf-generic-instrumentation
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-generic-instrumentation.git
RUN mkdir enterprise-workspace && pip install --target enterprise-workspace ./ebpf-generic-instrumentation/dtrace-injector

FROM node:16 AS nodejs-builder
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

# Dtrace injector
FROM keyval/javabuilder-base:v1.0 AS java-builder
WORKDIR /dtrace-injector
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/odigos-io/ebpf-java-instrumentation.git
WORKDIR /dtrace-injector/ebpf-java-instrumentation/utils
RUN mvn clean package -DskipTests
RUN mkdir -p ../dtrace-injector/target && cp ./target/utils.jar ../dtrace-injector/target/
WORKDIR /dtrace-injector/ebpf-java-instrumentation/dtrace-injector
ENV JAVA_HOME=/usr/local/openjdk-8
RUN make generate-headers
RUN make build-jni && mvn package -DskipTests

FROM --platform=$BUILDPLATFORM keyval/odiglet-base:v1.5 as builder

RUN --mount=type=secret,id=github_token \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"

WORKDIR /go/src/github.com/odigos-io/odigos-enterprise
# Copyy local modules required by the build
COPY odigosauth/ odigosauth/
COPY distros/ distros/
WORKDIR ./odiglet/
ENV GOPRIVATE=github.com/odigos-io/enterprise-go-instrumentation,github.com/odigos-io/ebpf-java-instrumentation
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

# Java
ARG JAVA_OTEL_VERSION=v1.29.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# Python
COPY --from=python-builder /python-instrumentation/workspace /instrumentations/python

# Python eBPF
COPY --from=python-builder /python-instrumentation/enterprise-workspace /instrumentations/python-ebpf

# NodeJS eBPF
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/dist /instrumentations/nodejs-ebpf/dist
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/lib /instrumentations/nodejs-ebpf/lib
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/build/Release/ /instrumentations/nodejs-ebpf/build/Release/
COPY --from=nodejs-builder /nodejs-instrumentation/ebpf-nodejs-instrumentation/dtrace-injector/node_modules /instrumentations/nodejs-ebpf/node_modules

# .NET
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

# Java eBPF
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/tracing_probes.so /instrumentations/java-ebpf/tracing_probes.so
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/dtrace-injector-1.0-SNAPSHOT.jar /instrumentations/java-ebpf/dtrace-injector.jar
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/utils.jar /instrumentations/java-ebpf/utils.jar

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/odigos-io/odigos-enterprise/odiglet/odiglet /root/odiglet
COPY --from=builder /go/bin/dlv /root/dlv
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .

EXPOSE 2345
ENTRYPOINT ["/root/dlv" ,"--listen=:2345", "--headless=true", "--api-version=2", "--accept-multiclient", "exec", "/root/odiglet"]

