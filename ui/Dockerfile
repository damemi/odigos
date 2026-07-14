# Enterprise UI image.
#
# Built differently from the other enterprise binaries because it bundles a
# Next.js webapp on top of the Go binary. The shared root Dockerfile only does
# Go builds; this one adds a Node.js webapp stage that clones the OSS repo at
# the pinned ODIGOS_OSS_REF, runs `yarn build`, and copies the resulting bundle
# into the Go build context so the //go:embed directive in cmd/main.go resolves.
#
# Image: registry.odigos.io/odigos-enterprise-ui:$TAG (matches helm chart's
#        enterprise-ui Component name, swapped in by odigos.secretExists).

ARG ODIGOS_OSS_REF
# ODIGOS_OSS_REF is supplied by Makefile's build-image/% template (--build-arg
# ODIGOS_OSS_REF=$(TAG)). When the TAG happens to be a non-existent git ref
# (e.g. during a build pre-tag), override with --build-arg ODIGOS_OSS_REF=main.

############################################################
# 1) Webapp stage — clone OSS at the pinned ref, yarn-build
############################################################
FROM --platform=$BUILDPLATFORM node:20.19.0-alpine AS webapp
ARG ODIGOS_OSS_REF
RUN apk add --no-cache git
WORKDIR /src
RUN git clone --depth 1 --branch ${ODIGOS_OSS_REF} https://github.com/odigos-io/odigos.git oss
WORKDIR /src/oss/frontend/webapp
RUN yarn install --frozen-lockfile && yarn build
# /src/oss/frontend/webapp/out/ now contains the React/Next.js bundle.

############################################################
# 2) Go builder — needs the OSS source tree for the replace
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
# point a path-based replace at /workspace/oss/<module>). We use the same
# clone the webapp stage produced.
COPY --from=webapp /src/oss /workspace/oss

# Enterprise UI source.
WORKDIR /workspace/$SERVICE_NAME
COPY $SERVICE_NAME/ .

RUN go mod tidy && \
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
