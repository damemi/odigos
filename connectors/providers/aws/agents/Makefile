# Build and push the Fargate agents image (env-driven OpenTelemetry agents staged by the AWS connector's
# ECS Fargate instrumentor). Fargate pulls from any registry the task has egress to; the default is the
# Odigos registry. Fargate tasks run on amd64 AND arm64 (Graviton), so the published image must be a
# multi-arch manifest list — use buildx-push for releases.
#
# The repo-root Makefile wires this into the AWS connector build: `make build-connector-aws` depends on
# `build-fargate-agents` and `make push-connector-aws` on `push-fargate-agents`, both delegating here
# with REGISTRY=$(ORG) IMAGE=odigos-fargate-agents$(IMG_SUFFIX) TAG=$(TAG) so the bundle rides the same
# ORG/TAG as the connector release. Standalone invocations here default to the stable bundle tag below.
#
# Usage:
#   make build                  # single-platform local build (host arch) for development
#   make push                   # push the locally-built tag
#   make build-push             # build then push (single platform)
#   make buildx-push            # multi-arch build+push (linux/amd64,linux/arm64) — use for releases
#   make buildx-push TAG=v0.2.0 # override the tag
#   make print                  # show the fully-qualified image ref

REGISTRY  ?= registry.odigos.io
IMAGE     ?= odigos-fargate-agents
TAG       ?= v0.1.0
PLATFORMS ?= linux/amd64,linux/arm64

IMG := $(REGISTRY)/$(IMAGE):$(TAG)

# Optional agent-version overrides (forwarded as Docker build args).
JAVA_OTEL_VERSION   ?=
NODEJS_OTEL_VERSION ?=
DOTNET_OTEL_VERSION ?=

BUILD_ARGS :=
ifneq ($(JAVA_OTEL_VERSION),)
BUILD_ARGS += --build-arg JAVA_OTEL_VERSION=$(JAVA_OTEL_VERSION)
endif
ifneq ($(NODEJS_OTEL_VERSION),)
BUILD_ARGS += --build-arg NODEJS_OTEL_VERSION=$(NODEJS_OTEL_VERSION)
endif
ifneq ($(DOTNET_OTEL_VERSION),)
BUILD_ARGS += --build-arg DOTNET_OTEL_VERSION=$(DOTNET_OTEL_VERSION)
endif

.PHONY: build push build-push buildx-push print

# Local single-platform build (loads into the local Docker engine; host architecture).
build:
	docker build $(BUILD_ARGS) -t $(IMG) .

push:
	docker push $(IMG)

build-push: build push

# Multi-arch build that pushes the manifest list directly (requires a buildx builder).
buildx-push:
	docker buildx build --platform $(PLATFORMS) $(BUILD_ARGS) -t $(IMG) --push .

print:
	@echo $(IMG)
