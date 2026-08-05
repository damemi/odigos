# Fargate agents image (odigos-fargate-agents).
#
# A self-contained image holding the OpenTelemetry language agents the ECS Fargate instrumentor stages
# into each task's shared volume: the injected odigos-agent sidecar (essential=false) copies
# /instrumentations/<lang> into the task volume mounted at /var/odigos, exits, and the app container —
# which dependsOn it with condition SUCCESS — starts with the agent-activation env pointing at the staged
# files. Fargate is serverless: there is no odiglet and no OpAMP server, so every agent here must be
# PURELY ENV-DRIVEN (configured by the OTEL_* env vars the connector injects). For that reason Node uses
# the upstream @opentelemetry/auto-instrumentations-node rather than the Odigos nodejs-community distro,
# which is OpAMP-driven and skips startup without ODIGOS_OPAMP_SERVER_HOST.
#
# The final stage is BusyBox (provides sh/cp for the copier sidecar) and runs as root so the sidecar can
# always read and copy the agent files regardless of their ownership/permissions.
#
# Multi-arch: Fargate runs both amd64 and arm64 (Graviton) tasks and pulls the manifest-list entry
# matching the task's RuntimePlatform.CpuArchitecture, so this image is published as a manifest list
# (see the Makefile buildx-push target). Java/Node/Python/PHP/Ruby agent stages resolve per-platform via
# their upstream/multi-arch sources; the .NET stage picks the arch-matching glibc zip via TARGETARCH and
# renames it to the arch-invariant linux-glibc path the injected CORECLR_PROFILER_PATH expects.
#
# Unlike Cloud Run, ECS has no registry restriction — tasks just need image-pull egress (public subnet or
# NAT) to the registry this is published to (registry.odigos.io by default).

ARG TARGETARCH

# v2.21.0 was the first release with official JDK 25 support (older Byte Buddy rejects Java 25+ class
# files, failing every transformation); keep this recent so bleeding-edge JDKs auto-instrument.
ARG JAVA_OTEL_VERSION=v2.29.0
ARG NODEJS_OTEL_VERSION=0.78.0
ARG DOTNET_OTEL_VERSION=v1.9.0

######### java (upstream javaagent; a jar, architecture-independent) #########
FROM busybox:1.36.1 AS java
ARG JAVA_OTEL_VERSION
# Plain ADD creates /instrumentations/java at the default 0755 (traversable); chmod only the jar. Do NOT use
# `ADD --chmod=644`: it also clamps the created parent dir to 0644 (no execute bit), which blocks non-root
# reads.
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/${JAVA_OTEL_VERSION}/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

######### nodejs (upstream, env-driven auto-instrumentation; pure JS, architecture-independent) #########
FROM node:22-bookworm-slim AS nodejs
ARG NODEJS_OTEL_VERSION
WORKDIR /instrumentations/nodejs
# Activated via NODE_OPTIONS=--require .../@opentelemetry/auto-instrumentations-node/build/src/register.js
RUN npm init -y >/dev/null 2>&1 && \
    npm install --omit=dev @opentelemetry/auto-instrumentations-node@${NODEJS_OTEL_VERSION}

######### dotnet (upstream automatic instrumentation, glibc, per-arch) #########
# One stage per architecture fetching the matching glibc zip; the arch-neutral `dotnet` stage below
# selects one via TARGETARCH, so a buildx multi-platform build picks the right native profiler.
FROM busybox:1.36.1 AS dotnet-fetch-amd64
ARG DOTNET_OTEL_VERSION
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-x64.zip /tmp/dotnet.zip

FROM busybox:1.36.1 AS dotnet-fetch-arm64
ARG DOTNET_OTEL_VERSION
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-arm64.zip /tmp/dotnet.zip

FROM dotnet-fetch-${TARGETARCH} AS dotnet
WORKDIR /instrumentations/dotnet
# The zip lays down net/, AdditionalDeps/, store/ and linux-{x64|arm64}/; rename the arch dir to the
# arch-invariant linux-glibc the connector's CORECLR_PROFILER_PATH expects
# (/var/odigos/dotnet/linux-glibc/...Native.so), so the injected env is identical on both architectures.
RUN unzip -q /tmp/dotnet.zip -d . && rm /tmp/dotnet.zip && \
    for d in linux-x64 linux-arm64; do [ -d "$d" ] && mv "$d" linux-glibc || true; done

######### final: env-driven agents bundle #########
FROM busybox:1.36.1
WORKDIR /instrumentations
COPY --from=java /instrumentations/java /instrumentations/java
COPY --from=nodejs /instrumentations/nodejs /instrumentations/nodejs
COPY --from=dotnet /instrumentations/dotnet /instrumentations/dotnet
# Python uses the Odigos python-community distro, which is env-driven (exports OTLP from OTEL_* without an
# OpAMP server). PHP and Ruby use the community native SDK bundles (env-driven, per-runtime-version
# subdirectories — the injected PHP_INI_SCAN_DIR/RUBYOPT select the major.minor path). These per-language
# agent images are the same ones the OSS odiglet agents image composes from.
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.90 /instrumentations/python /instrumentations/python
COPY --from=public.ecr.aws/odigos/agents/php-community:v0.4.1 /instrumentations/php /instrumentations/php
COPY --from=public.ecr.aws/odigos/agents/ruby-community:v0.0.8 /instrumentations/ruby /instrumentations/ruby
# No USER directive: run as root so the copier sidecar can always read the agent files (see header).
