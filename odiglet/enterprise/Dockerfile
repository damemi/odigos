FROM python:3.11 AS python-builder
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install --target workspace -r requirements.txt

# Enterprise version of ebpf-python-instrumentation
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/keyval-dev/ebpf-python-instrumentation.git
RUN mkdir enterprise-workspace && pip install --target enterprise-workspace ./ebpf-python-instrumentation/dtrace-injector

FROM node:21.6.2-bullseye AS nodejs-builder
WORKDIR /nodejs-instrumentation
RUN apt update -y && apt install -y systemtap-sdt-dev
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/keyval-dev/ebpf-nodejs-instrumentation.git \
    && cd ebpf-nodejs-instrumentation/dtrace-injector \
    && make build

FROM busybox AS dotnet-builder
WORKDIR /dotnet-instrumentation
ARG DOTNET_OTEL_VERSION=v0.7.0
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/$DOTNET_OTEL_VERSION/opentelemetry-dotnet-instrumentation-linux-musl.zip .
RUN unzip opentelemetry-dotnet-instrumentation-linux-musl.zip && rm opentelemetry-dotnet-instrumentation-linux-musl.zip

# Dtrace injector
FROM keyval/javabuilder-base:v1.0 AS java-builder
WORKDIR /dtrace-injector
RUN --mount=type=secret,id=github_token \
    git clone https://$(cat /run/secrets/github_token)@github.com/keyval-dev/ebpf-java-instrumentation.git
WORKDIR /dtrace-injector/ebpf-java-instrumentation/utils
RUN mvn clean package -DskipTests
RUN mkdir -p ../dtrace-injector/target && cp ./target/utils.jar ../dtrace-injector/target/
WORKDIR /dtrace-injector/ebpf-java-instrumentation/dtrace-injector
ENV JAVA_HOME=/usr/local/openjdk-8
RUN make generate-headers
RUN make build-jni && mvn package -DskipTests

FROM --platform=$BUILDPLATFORM keyval/odiglet-base:v1.3 as builder

RUN --mount=type=secret,id=github_token \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/"

WORKDIR /go/src/github.com/keyval-dev/odigos-enterprise
# Copyy local modules required by the build
COPY odigosauth/ odigosauth/
WORKDIR ./odiglet/
ENV GOPRIVATE=github.com/keyval-dev/enterprise-go-instrumentation,github.com/keyval-dev/ebpf-java-instrumentation
# Pre-copy/cache go.mod for pre-downloading dependencies and only redownloading
COPY odiglet/go.mod odiglet/go.sum ./
RUN go mod download
# Copy rest of source code for odiglet
COPY odiglet/ .

ARG TARGETARCH
# Go does not call go generate on dependencies, so we need to do it manually
RUN cd $(go list -m -f '{{.Dir}}' "go.opentelemetry.io/auto") && make generate
RUN cd $(go list -m -f '{{.Dir}}' "github.com/keyval-dev/ebpf-java-instrumentation") && make generate
RUN cd $(go list -m -f '{{.Dir}}' "github.com/keyval-dev/ebpf-generic-instrumentation") && make generate
RUN cd $(go list -m -f '{{.Dir}}' "github.com/keyval-dev/ebpf-nodejs-instrumentation") && make generate

RUN GOOS=linux GOARCH=$TARGETARCH go build -o odiglet ./cmd/enterprise

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
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/dtrace-injector-1.0-SNAPSHOT-full.jar /instrumentations/java-ebpf/dtrace-injector.jar
COPY --from=java-builder /dtrace-injector/ebpf-java-instrumentation/dtrace-injector/target/utils.jar /instrumentations/java-ebpf/utils.jar

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/keyval-dev/odigos-enterprise/odiglet/odiglet /root/odiglet
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .
CMD ["/root/odiglet"]
