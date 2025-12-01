TAG ?= $(shell odigos version --cluster)

# Include dependency update targets
include deps.mk
# These are used to point to different Dockerfiles (such as Dockerfile.rhel) and add -ubi9 to RHEL based images.
DOCKERFILE=Dockerfile
IMG_PREFIX?=
IMG_SUFFIX?=
ORG ?= registry.odigos.io
BUILD_DIR=.
# If you made a change and it's not being reflected in the image,
# it might be because the cache is being used and not invalidated properly.
# Use NO_CACHE=--no-cache to force a rebuild, or make build-odiglet-with-otel_agents NO_CACHE=--no-cache
NO_CACHE?=

.PHONY: build-image/%
build-image/%:
	docker build -t $(ORG)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG) $(BUILD_DIR) -f $(DOCKERFILE) \
	--secret id=github_token,env=GITHUB_TOKEN \
	--build-arg SERVICE_NAME="$*" \
	--build-arg ODIGOS_VERSION=$(TAG) \
	--build-arg SUMMARY="Odiglet for Odigos" \
	--build-arg DESCRIPTION="Odiglet is the core component of Odigos managing auto-instrumentation. This container requires a root user to run and manage eBPF programs." \
	--build-arg VERSION=$(TAG) \
	--build-arg RELEASE=$(TAG) \
	--build-arg LD_FLAGS="$(LD_FLAGS)" \
	$(BUILD_CONTEXT) \
	$(NO_CACHE)

.PHONY: build-odiglet
build-odiglet:
	$(MAKE) build-image/odiglet DOCKERFILE=odiglet/$(DOCKERFILE) SUMMARY="Odiglet for Odigos" DESCRIPTION="Odiglet is the core component of Odigos managing auto-instrumentation. This container requires a root user to run and manage eBPF programs." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-odiglet-with-otel_agents
build-odiglet-with-otel_agents:
	$(MAKE) build-image/odiglet DOCKERFILE=odiglet/$(DOCKERFILE) SUMMARY="Odiglet for Odigos" DESCRIPTION="Odiglet is the core component of Odigos managing auto-instrumentation. This container requires a root user to run and manage eBPF programs." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) BUILD_CONTEXT="--build-context nodejs-agent-src=../opentelemetry-node --build-context nodejs-enterprise-agent-src=../ebpf-nodejs-instrumentation" NO_CACHE=--no-cache

.PHONY: build-instrumentor
build-instrumentor:
	$(MAKE) build-image/instrumentor SUMMARY="Instrumentor for Odigos" DESCRIPTION="Instrumentor manages auto-instrumentation for workloads with Odigos." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-central-backend
build-central-backend:
	$(MAKE) build-image/central-backend SUMMARY="Central Backend for Odigos" DESCRIPTION="Central Backend is the core component of Odigos Central, responsible for managing the data flow from the cluster to the ui." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-central-ui
build-central-ui:
	$(MAKE) build-image/central-ui DOCKERFILE=central-ui/$(DOCKERFILE) BUILD_DIR=central-ui SUMMARY="Central UI for Odigos" DESCRIPTION="Central UI is the web interface for Odigos Central, providing a user-friendly way to manage and visualize the data collected by Odigos." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-central-proxy
build-central-proxy:
	$(MAKE) build-image/central-proxy SUMMARY="Central Proxy for Odigos" DESCRIPTION="Central Proxy is handle the communication between the cluster that Odigos is running in and the Odigos Central." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-agents
build-agents:
	$(MAKE) build-image/agents DOCKERFILE=agents/$(DOCKERFILE) SUMMARY="Odigos Enterprise Agents" DESCRIPTION="The Odigos Agents used to copy Odigos agent relevant files into the user workloads." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-images
build-images:
	make -j 2 build-odiglet build-instrumentor TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) DOCKERFILE=$(DOCKERFILE)

.PHONY: build-central-images
build-central-images:
	make -j 3 build-central-backend build-central-ui build-central-proxy TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) DOCKERFILE=$(DOCKERFILE)

.PHONY: build-images-with-agents
build-images-with-agents:
	make -j 2 build-odiglet-with-otel_agents build-instrumentor TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) DOCKERFILE=$(DOCKERFILE)

.PHONY: build-images-rhel
build-images-rhel:
	$(MAKE) build-images IMG_SUFFIX=-ubi9 DOCKERFILE=Dockerfile.rhel TAG=$(TAG) ORG=$(ORG)

push-image/%:
	docker buildx build --platform linux/amd64,linux/arm64/v8 -t $(ORG)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG) $(BUILD_DIR) -f $(DOCKERFILE) \
	--secret id=github_token,env=GITHUB_TOKEN \
	--build-arg SERVICE_NAME="$*" \
	--build-arg VERSION=$(TAG) \
	--build-arg RELEASE=$(TAG) \
	--build-arg SUMMARY="$(SUMMARY)" \
	--build-arg DESCRIPTION="$(DESCRIPTION)"

.PHONY: push-odiglet
push-odiglet:
	$(MAKE) push-image/odiglet DOCKERFILE=odiglet/$(DOCKERFILE) SUMMARY="Odiglet for Odigos" DESCRIPTION="Odiglet is the core component of Odigos managing auto-instrumentation." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: push-instrumentor
push-instrumentor:
	$(MAKE) push-image/instrumentor SUMMARY="Instrumentor for Odigos" DESCRIPTION="Instrumentor manages auto-instrumentation for workloads with Odigos." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

load-to-kind-%:
	kind load docker-image $(ORG)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG)

.PHONY: load-to-kind
load-to-kind:
	make -j 2 load-to-kind-instrumentor load-to-kind-odiglet ORG=$(ORG) TAG=$(TAG) IMG_SUFFIX=$(IMG_SUFFIX) DOCKERFILE=$(DOCKERFILE)

.PHONY: load-central-to-kind
load-central-to-kind:
	make -j 3 load-to-kind-central-backend load-to-kind-central-ui load-to-kind-central-proxy ORG=$(ORG) TAG=$(TAG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: restart-odiglet
restart-odiglet:
	kubectl rollout restart daemonset odiglet -n odigos-system || true

.PHONY: restart-instrumentor
restart-instrumentor:
	kubectl rollout restart deployment odigos-instrumentor -n odigos-system || true

.PHONY: restart-central-backend
restart-central-backend:
	kubectl rollout restart deployment central-backend -n odigos-central || true

.PHONY: restart-central-ui
restart-central-ui:
	kubectl rollout restart deployment central-ui -n odigos-central || true

.PHONY: restart-central-proxy
restart-central-proxy:
	kubectl rollout restart deployment central-proxy -n odigos-central || true

.PHONY: restart-all
restart-all:
	make restart-odiglet && make restart-instrumentor && make restart-central-backend && make restart-central-ui && make restart-central-proxy

.PHONY: restart-central
restart-central:
	make restart-central-backend && make restart-central-ui && make restart-central-proxy

.PHONY: deploy-odiglet
deploy-odiglet:
	make build-odiglet TAG=$(TAG) && make load-to-kind-odiglet TAG=$(TAG) && make restart-odiglet

.PHONY: deploy-odiglet-with-agents
deploy-odiglet-with-agents:
	make build-odiglet-with-otel_agents TAG=$(TAG) && make load-to-kind-odiglet TAG=$(TAG) && make restart-odiglet

.PHONY: deploy-instrumentor
deploy-instrumentor:
	make build-instrumentor TAG=$(TAG) && make load-to-kind-instrumentor TAG=$(TAG) && make restart-instrumentor

.PHONY: deploy-agents
deploy-agents:
	make build-agents TAG=$(TAG) && make load-to-kind-agents TAG=$(TAG)

.PHONY: deploy-central-backend
deploy-central-backend:
	make build-central-backend TAG=$(TAG) && make load-to-kind-central-backend TAG=$(TAG) && make restart-central-backend

.PHONY: deploy-central-ui
deploy-central-ui:
	make build-central-ui TAG=$(TAG) && make load-to-kind-central-ui TAG=$(TAG) && make restart-central-ui

.PHONY: deploy-central-proxy
deploy-central-proxy:
	make build-central-proxy TAG=$(TAG) && make load-to-kind-central-proxy TAG=$(TAG) && make restart-central-proxy

.PHONY: deploy
deploy:
	make build-images TAG=$(TAG) && make load-to-kind TAG=$(TAG) && make restart-odiglet && make restart-instrumentor

.PHONY: deploy-central
deploy-central:
	make build-central-images TAG=$(TAG) && make load-central-to-kind TAG=$(TAG) && make restart-central

# Use these to deploy Odigos Enterprise into an EKS cluster

.PHONY: ecr-login
ecr-login:
	aws ecr-public get-login-password --region us-east-1 | docker login --username AWS --password-stdin public.ecr.aws

build-tag-push-ecr-image/%:
	docker build --platform linux/amd64 -t $(ORG)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG) $(BUILD_DIR) -f $(DOCKERFILE) \
	--secret id=github_token,env=GITHUB_TOKEN \
	--build-arg SERVICE_NAME="$*" \
	--build-arg ODIGOS_VERSION=$(TAG) \
	--build-arg VERSION=$(TAG) \
	--build-arg RELEASE=$(TAG) \
	--build-arg SUMMARY="$(SUMMARY)" \
	--build-arg DESCRIPTION="$(DESCRIPTION)"
	docker tag $(ORG)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG) $(IMG_PREFIX)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG)
	docker push $(IMG_PREFIX)/odigos-enterprise-$*$(IMG_SUFFIX):$(TAG)

.PHONY: publish-to-ecr
publish-to-ecr:
	if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set"; \
		exit 1; \
	fi
	if [ -z "$(IMG_PREFIX)" ]; then \
		echo "❌ IMG_PREFIX is not set"; \
		exit 1; \
	fi
	make ecr-login
	make -j 3 build-tag-push-ecr-image/odiglet DOCKERFILE=odiglet/$(DOCKERFILE) SUMMARY="Odiglet for Odigos" DESCRIPTION="Odiglet is the core component of Odigos managing auto-instrumentation. This container requires a root user to run and manage eBPF programs." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	make -j 3 build-tag-push-ecr-image/instrumentor SUMMARY="Instrumentor for Odigos" DESCRIPTION="Instrumentor manages auto-instrumentation for workloads with Odigos." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	make -j 3 build-tag-push-ecr-image/central-backend SUMMARY="Central Backend for Odigos" DESCRIPTION="Central Backend is the core component of Odigos Central, responsible for managing the data flow from the cluster to the ui." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	make -j 3 build-tag-push-ecr-image/central-ui BUILD_DIR=central-ui DOCKERFILE=central-ui/$(DOCKERFILE) SUMMARY="Central UI for Odigos" DESCRIPTION="Central UI is the web interface for Odigos Central, providing a user-friendly way to manage and visualize the data collected by Odigos." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	make -j 3 build-tag-push-ecr-image/central-proxy SUMMARY="Central Proxy for Odigos" DESCRIPTION="Central Proxy is handle the communication between the cluster that Odigos is running in and the Odigos Central." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	echo "✅ Publish Odigos images into ECR, now install the CLI"

ALL_GO_MOD_DIRS := $(shell find . -type f -name 'go.mod' -exec dirname {} \; | sort | grep -v "licenses")

.PHONY: go-mod-tidy
go-mod-tidy: $(ALL_GO_MOD_DIRS:%=go-mod-tidy/%)
go-mod-tidy/%: DIR=$*
go-mod-tidy/%:
	@cd $(DIR) && go mod tidy

# Debug

.PHONY: debug-odiglet
debug-odiglet:
	docker build -t $(ORG)/odigos-enterprise-odiglet:$(TAG) . -f odiglet/debug.Dockerfile --secret id=github_token,env=GITHUB_TOKEN
	kind load docker-image $(ORG)/odigos-enterprise-odiglet:$(TAG)
	kubectl delete pod -n odigos-system -l app.kubernetes.io/name=odiglet
	kubectl wait --for=condition=ready pod -n odigos-system -l app.kubernetes.io/name=odiglet
	kubectl port-forward -n odigos-system daemonset/odiglet 2345:2345
