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
RUN ODIGOS_OSS_REF=$(grep -E '^replace github.com/odigos-io/odigos/frontend =>' /tmp/ui.go.mod | awk '{print $NF}') && \
    test -n "$ODIGOS_OSS_REF" && \
    CHECKOUT_REF=$(echo "$ODIGOS_OSS_REF" | sed -nE 's/.*[0-9]{14}-([0-9a-f]{12})$/\1/p') && \
    if [ -z "$CHECKOUT_REF" ]; then \
      CHECKOUT_REF="$ODIGOS_OSS_REF"; \
    fi && \
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
# 3) Webapp stage — yarn-build the Next.js frontend from oss-src
############################################################
FROM --platform=$BUILDPLATFORM node:24-alpine AS webapp
WORKDIR /src/oss/frontend
COPY --from=oss-src /frontend .
WORKDIR /src/oss/frontend/webapp
RUN yarn install --frozen-lockfile && yarn build
# /src/oss/frontend/webapp/out/ now contains the React/Next.js bundle.

############################################################
# 4) Go builder — needs the OSS source tree for the replace
#    directive (github.com/odigos-io/odigos/frontend => /workspace/oss/frontend).
#    The webapp bundle + /workloads page embed inside the OSS frontend module
#    itself (webapp + graph packages), so compiling those packages from the
#    yarn-built clone is all that's needed — this image embeds nothing of its own.
############################################################
FROM --platform=$BUILDPLATFORM golang:1.26.4 AS builder
ARG SERVICE_NAME=ui
ARG TARGETARCH
ARG LD_FLAGS

# Copy enterprise sibling modules required by the build (mirrors the shared
# Dockerfile pattern — see odigos-enterprise/Dockerfile lines 6-7).
WORKDIR /workspace
COPY distros/ distros/
COPY odigosauth/ odigosauth/
COPY mcp/ mcp/

# Pull OSS into /workspace/oss (full source so go's replace directive can
# point a path-based replace at /workspace/oss/<module>). We use the yarn-built
# tree from the webapp stage so //go:embed picks up webapp/out/.
COPY --from=webapp /src/oss /workspace/oss

# Enterprise UI source.
WORKDIR /workspace/$SERVICE_NAME
COPY $SERVICE_NAME/ .
RUN go mod edit \
        -replace=github.com/odigos-io/odigos/frontend=/workspace/oss/frontend && \
    go mod tidy && \
    go mod download && go mod verify

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
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
