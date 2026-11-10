# Cloud Run agents image.
#
# A self-contained image holding the OpenTelemetry language agents the Cloud Run instrumentor stages into
# each service's shared in-memory volume. Cloud Run is serverless: there is no odiglet and no OpAMP server,
# so every agent here must be PURELY ENV-DRIVEN (configured by the OTEL_* env vars the connector injects).
# For that reason Node uses the upstream @opentelemetry/auto-instrumentations-node rather than the Odigos
# nodejs-community distro, which is OpAMP-driven and skips startup without ODIGOS_OPAMP_SERVER_HOST.
#
# The final stage is BusyBox (provides sh/cp/nc for the copier sidecar) and runs as root: Cloud Run's v2
# API exposes no securityContext/runAsUser, so running as root guarantees the sidecar can always read and
# copy the agent files regardless of their ownership/permissions.
#
# Build + push (Cloud Run pulls only from *.gcr.io, *-docker.pkg.dev or docker.io):
#   docker build --platform linux/amd64 -t us-central1-docker.pkg.dev/odigos-cloud/components/cloud-run-agents:v0.1.0 .
#   docker push us-central1-docker.pkg.dev/odigos-cloud/components/cloud-run-agents:v0.1.0

# v2.21.0 was the first release with official JDK 25 support (older Byte Buddy rejects Java 25+ class
# files, failing every transformation); keep this recent so bleeding-edge JDKs auto-instrument.
ARG JAVA_OTEL_VERSION=v2.29.0
ARG NODEJS_OTEL_VERSION=0.78.0
ARG DOTNET_OTEL_VERSION=v1.9.0

######### java (upstream javaagent) #########
FROM busybox:1.36.1 AS java
ARG JAVA_OTEL_VERSION
# Plain ADD creates /instrumentations/java at the default 0755 (traversable); chmod only the jar. Do NOT use
# `ADD --chmod=644`: it also clamps the created parent dir to 0644 (no execute bit), which blocks non-root
# reads (the historical Java-on-Cloud-Run breakage).
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/${JAVA_OTEL_VERSION}/opentelemetry-javaagent.jar /instrumentations/java/javaagent.jar
RUN chmod 644 /instrumentations/java/javaagent.jar

######### nodejs (upstream, env-driven auto-instrumentation) #########
FROM node:22-bookworm-slim AS nodejs
ARG NODEJS_OTEL_VERSION
WORKDIR /instrumentations/nodejs
# Pure-JS instrumentations, so the install is architecture-independent. Activated via
# NODE_OPTIONS=--require .../@opentelemetry/auto-instrumentations-node/build/src/register.js
RUN npm init -y >/dev/null 2>&1 && \
    npm install --omit=dev @opentelemetry/auto-instrumentations-node@${NODEJS_OTEL_VERSION}

######### dotnet (upstream automatic instrumentation, glibc/x64) #########
# Cloud Run's managed runtime is glibc/amd64, so only the linux-glibc x64 build is included.
FROM busybox:1.36.1 AS dotnet
ARG DOTNET_OTEL_VERSION
WORKDIR /instrumentations/dotnet
ADD https://github.com/open-telemetry/opentelemetry-dotnet-instrumentation/releases/download/${DOTNET_OTEL_VERSION}/opentelemetry-dotnet-instrumentation-linux-glibc-x64.zip /tmp/dotnet.zip
# The zip lays down net/, AdditionalDeps/, store/ and linux-x64/; rename linux-x64 -> linux-glibc to match
# the CORECLR_PROFILER_PATH the connector injects (/var/odigos/dotnet/linux-glibc/...Native.so).
RUN unzip -q /tmp/dotnet.zip -d . && rm /tmp/dotnet.zip && mv linux-x64 linux-glibc

######### final: env-driven agents bundle #########
FROM busybox:1.36.1
WORKDIR /instrumentations
COPY --from=java /instrumentations/java /instrumentations/java
COPY --from=nodejs /instrumentations/nodejs /instrumentations/nodejs
COPY --from=dotnet /instrumentations/dotnet /instrumentations/dotnet
# Python uses the Odigos python-community distro, which is env-driven (exports OTLP from OTEL_* without an
# OpAMP server).
COPY --from=public.ecr.aws/odigos/agents/python-community:v1.0.90 /instrumentations/python /instrumentations/python
# No USER directive: run as root so the copier sidecar can always read the agent files (see header).
