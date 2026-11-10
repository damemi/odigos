# Build and push the Cloud Run agents image (env-driven OpenTelemetry agents staged by the GCP connector's
# Cloud Run instrumentor). Cloud Run pulls only from *.gcr.io, *-docker.pkg.dev (Artifact Registry) or
# docker.io, so REGISTRY must be one of those.
#
# Usage:
#   make build                 # build for linux/amd64 (Cloud Run's runtime)
#   make push                  # push the built tag
#   make build-push            # build then push
#   make build-push TAG=v0.2.0 # override the tag
#   make print                 # show the fully-qualified image ref

REGISTRY ?= us-central1-docker.pkg.dev/odigos-cloud/components
IMAGE    ?= cloud-run-agents
TAG      ?= v0.1.0
PLATFORM ?= linux/amd64

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

# Build for the Cloud Run runtime platform and load the image into the local Docker engine.
build:
	docker build --platform $(PLATFORM) $(BUILD_ARGS) -t $(IMG) .

push:
	docker push $(IMG)

build-push: build push

# One-shot cross-platform build that pushes directly (requires a buildx builder).
buildx-push:
	docker buildx build --platform $(PLATFORM) $(BUILD_ARGS) -t $(IMG) --push .

print:
	@echo $(IMG)
