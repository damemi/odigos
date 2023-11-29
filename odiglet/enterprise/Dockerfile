# syntax=docker/dockerfile:1.3

FROM python:3.11 AS python-builder
WORKDIR /python-instrumentation
ADD odiglet/agents/python/requirements.txt .
RUN mkdir workspace && pip install --target workspace -r requirements.txt

FROM node:16 AS nodejs-builder
WORKDIR /nodejs-instrumentation
COPY odiglet/agents/nodejs .
RUN npm install

FROM busybox AS dotnet-builder
WORKDIR /dotnet-instrumentation
ARG DOTNET_OTEL_VERSION=v0.7.0
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/$DOTNET_OTEL_VERSION/opentelemetry-dotnet-instrumentation-linux-musl.zip .
RUN unzip opentelemetry-dotnet-instrumentation-linux-musl.zip && rm opentelemetry-dotnet-instrumentation-linux-musl.zip

FROM keyval/odiglet-base:v1.0 as builder
ARG TARGETARCH
RUN dnf install git -y
RUN git config --global url."git@github.com:".insteadOf "https://github.com/"
# Add GitHub's host key to known_hosts
RUN mkdir -p -m 0700 ~/.ssh && ssh-keyscan github.com >> ~/.ssh/known_hosts
RUN curl -LO https://go.dev/dl/go1.21.0.linux-${TARGETARCH}.tar.gz && tar -C /usr/local -xzf go*.linux-${TARGETARCH}.tar.gz
# Forward SSH agent for Git operations
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPRIVATE=github.com/keyval-dev/enterprise-go-instrumentation
WORKDIR /go/src/github.com/keyval-dev/odigos-enterprise
COPY . .
WORKDIR ./odiglet/
RUN --mount=type=ssh go mod download
# Go does not call go generate on dependencies, so we need to do it manually
RUN cd $(go list -m -f '{{.Dir}}' "go.opentelemetry.io/auto") && make generate
RUN GOOS=linux go build -o odiglet ./cmd/enterprise

WORKDIR /instrumentations

# Java
ARG JAVA_OTEL_VERSION=v1.29.0
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/$JAVA_OTEL_VERSION/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

# Python
COPY --from=python-builder /python-instrumentation/workspace /instrumentations/python

# NodeJS
COPY --from=nodejs-builder /nodejs-instrumentation/build/workspace /instrumentations/nodejs

# .NET
COPY --from=dotnet-builder /dotnet-instrumentation /instrumentations/dotnet

FROM registry.fedoraproject.org/fedora-minimal:38
COPY --from=builder /go/src/github.com/keyval-dev/odigos-enterprise/odiglet/odiglet /root/odiglet
WORKDIR /instrumentations/
COPY --from=builder /instrumentations/ .
CMD ["/root/odiglet"]
