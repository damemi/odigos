# syntax=docker/dockerfile:1

# Enterprise UI image.
#
# Built differently from the other enterprise binaries because it bundles a
# Next.js webapp on top of the Go binary. The shared root Dockerfile only does
# Go builds; this one adds a Node.js webapp stage that builds the OSS frontend
# webapp (yarn build) and copies the resulting bundle into the Go build context
# so the //go:embed directive in cmd/main.go resolves.
#
# OSS sources can come from either a git clone (default) or a local checkout via
# an additional build context — based on:
# https://www.docker.com/blog/dockerfiles-now-support-multiple-build-contexts/
#
# Image: registry.odigos.io/odigos-enterprise-ui:$TAG (matches helm chart's
#        enterprise-ui Component name, swapped in by odigos.secretExists).

ARG ODIGOS_OSS_WEBAPP_IMAGE=webapp-build

############################################################
# 1) Clone OSS from GitHub at the go.mod-pinned frontend ref
############################################################
FROM --platform=$BUILDPLATFORM alpine AS oss-clone
RUN apk add --no-cache git
WORKDIR /src
# Ref comes from: replace github.com/odigos-io/odigos/frontend => ... <version>
COPY ui/go.mod /tmp/ui.go.mod
# Supports both release tags (v1.32.0-pre5) and go-mod pseudo-versions.
# Pseudo-versions are not valid git refs, so extract the embedded 12-char
# commit. All forms end in <yyyymmddhhmmss>-<sha>:
#   v0.0.0-20260617121516-bfc5da9bb14b
#   v1.32.0-pre5.0.20260722102948-f2986a6dc9da  (based on a pre-release tag)
RUN --mount=type=secret,id=github_token \
    ODIGOS_OSS_REF=$(grep -E '^replace github.com/odigos-io/odigos/frontend =>' /tmp/ui.go.mod | awk '{print $NF}') && \
    test -n "$ODIGOS_OSS_REF" && \
    CHECKOUT_REF=$(echo "$ODIGOS_OSS_REF" | sed -nE 's/.*[0-9]{14}-([0-9a-f]{12})$/\1/p') && \
    if [ -z "$CHECKOUT_REF" ]; then \
      CHECKOUT_REF="$ODIGOS_OSS_REF"; \
    fi && \
    if [ -s /run/secrets/github_token ]; then git config --global url."https://$(cat /run/secrets/github_token):x-oauth-basic@github.com/".insteadOf "https://github.com/"; fi && \
    git clone --filter=blob:none https://github.com/odigos-io/odigos.git oss && \
    git -C oss checkout "$CHECKOUT_REF"

############################################################
# 2) Resolved OSS sources used by later stages.
#    By default this is the clone above. Override with a local checkout:
#      --build-context oss-src=../odigos
#    (see Makefile build-ui-local / ODIGOS_OSS_SRC)
############################################################
FROM scratch AS oss-src
COPY --from=oss-clone /src/oss /

############################################################
# 3) Build the webapp, or reuse an OSS frontend Dockerfile's builder image.
#    An image override must contain /webapp/out.
############################################################
FROM --platform=$BUILDPLATFORM node:24 AS webapp-build
WORKDIR /webapp
COPY --from=oss-src --exclude=**/node_modules --exclude=**/.next --exclude=**/out /frontend/webapp .
RUN --mount=type=cache,target=/usr/local/share/.cache/yarn \
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
# 4) Build Go against the same OSS checkout as the webapp.
#    Dependency modules' replace directives are not inherited by Go.
############################################################
FROM --platform=$BUILDPLATFORM golang:1.26.9 AS builder
ARG SERVICE_NAME=ui
ARG TARGETARCH
ARG LD_FLAGS

WORKDIR /workspace
COPY distros/ distros/
COPY odigosauth/ odigosauth/
COPY mcp/ mcp/

COPY --from=oss-src /actions /workspace/oss/actions
COPY --from=oss-src /api /workspace/oss/api
COPY --from=oss-src /common /workspace/oss/common
COPY --from=oss-src /config /workspace/oss/config
COPY --from=oss-src /destinations /workspace/oss/destinations
COPY --from=oss-src /distros /workspace/oss/distros
COPY --from=oss-src /instrumentationrules /workspace/oss/instrumentationrules
COPY --from=oss-src /k8sutils /workspace/oss/k8sutils
COPY --from=oss-src /odigosauth /workspace/oss/odigosauth
COPY --from=oss-src /profiles /workspace/oss/profiles
COPY --from=oss-src /recommendations /workspace/oss/recommendations
COPY --from=oss-src /status /workspace/oss/status
COPY --from=oss-src --exclude=**/webapp /frontend /workspace/oss/frontend
COPY --from=oss-src /frontend/webapp/embed.go /workspace/oss/frontend/webapp/embed.go
COPY --from=oss-webapp / /workspace/oss/frontend/webapp/out/
RUN test -s /workspace/oss/frontend/webapp/out/index.html

WORKDIR /workspace/$SERVICE_NAME
COPY $SERVICE_NAME/ .
RUN --mount=type=secret,id=github_token \
    --mount=type=cache,target=/go/pkg/mod \
    git config --global url."https://$(cat /run/secrets/github_token)@github.com/".insteadOf "https://github.com/" && \
    for module in /workspace/oss/*; do \
      go mod edit -replace=github.com/odigos-io/odigos/$(basename "$module")="$module"; \
    done && \
    go mod tidy && go mod download && go mod verify

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOARCH=$TARGETARCH \
    go build -ldflags="${LD_FLAGS}" -a -o /workspace/build/$SERVICE_NAME ./cmd

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
COPY --from=builder /workspace/$SERVICE_NAME/LICENSE ./licenses/.
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
