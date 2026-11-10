# syntax=docker/dockerfile:1

# Enterprise UI image.
#
# Bundles the frontend/webapp Next.js build into the Go binary, so the
# //go:embed directive in frontend/webapp/embed.go resolves.
#
# Image: registry.odigos.io/odigos-enterprise-ui:$TAG (matches helm chart's
#        enterprise-ui Component name, swapped in by odigos.secretExists).
# Build context: the repository root.

ARG ODIGOS_OSS_WEBAPP_IMAGE=webapp-build

############################################################
# 1) Build the webapp, or reuse an OSS frontend Dockerfile's builder image.
#    An image override must contain /webapp/out.
############################################################
FROM --platform=$BUILDPLATFORM node:24 AS webapp-build
WORKDIR /webapp
COPY --exclude=**/node_modules --exclude=**/.next --exclude=**/out frontend/webapp .
# The optional npmrc secret holds the GitHub Packages token for the @odigos-io scope (never copied into a layer).
RUN --mount=type=cache,target=/usr/local/share/.cache/yarn \
    --mount=type=secret,id=npmrc,target=/root/.npmrc,required=false \
    if [ -d .yalc ] && [ "$(ls -A .yalc 2>/dev/null)" ]; then \
      npm install -g yalc && yarn install; \
    else \
      yarn install --frozen-lockfile; \
    fi && yarn build

FROM ${ODIGOS_OSS_WEBAPP_IMAGE} AS webapp

# Override with --build-context oss-webapp=/path/to/frontend/webapp/out
# to embed an existing local bundle without running Node.js.
FROM scratch AS oss-webapp
COPY --from=webapp /webapp/out /

############################################################
# 2) Build Go. ui replaces these odigos modules with local paths.
############################################################
FROM --platform=$BUILDPLATFORM golang:1.26.9 AS builder
ARG SERVICE_NAME=ui
ARG TARGETARCH
ARG LD_FLAGS

WORKDIR /workspace
COPY actions/ actions/
COPY api/ api/
COPY common/ common/
COPY config/ config/
COPY destinations/ destinations/
COPY distros/ distros/
COPY instrumentationrules/ instrumentationrules/
COPY k8sutils/ k8sutils/
COPY mcp/ mcp/
COPY odigosauth/ odigosauth/
COPY profiles/ profiles/
COPY recommendations/ recommendations/
COPY status/ status/
COPY --exclude=webapp frontend/ frontend/
COPY frontend/webapp/embed.go frontend/webapp/embed.go
COPY --from=oss-webapp / frontend/webapp/out/
RUN test -s frontend/webapp/out/index.html

COPY ui/ ui/
WORKDIR /workspace/ui
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -o /workspace/build/$SERVICE_NAME ./cmd

############################################################
# RHEL variant (Red Hat certified image; mirrors shared Dockerfile)
############################################################
FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS rhel
ARG SERVICE_NAME=ui
ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION
LABEL "name"=$SERVICE_NAME
LABEL "vendor"="Odigos"
LABEL "maintainer"="Odigos"
LABEL "version"=$VERSION
LABEL "release"=$RELEASE
LABEL "summary"=$SUMMARY
LABEL "description"=$DESCRIPTION
WORKDIR /
COPY --from=builder /workspace/build/$SERVICE_NAME ./app
COPY --from=builder /workspace/ui/LICENSE ./licenses/.
USER 65532:65532
ENTRYPOINT ["/app"]

############################################################
# Final (distroless)
############################################################
FROM gcr.io/distroless/static:nonroot
ARG SERVICE_NAME=ui
WORKDIR /
COPY --from=builder /workspace/build/$SERVICE_NAME ./app
USER 65532:65532
ENTRYPOINT ["/app"]
