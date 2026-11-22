.PHONY: build-central-backend
build-central-backend:
	$(MAKE) build-image/central-backend DOCKERFILE=central-backend/Dockerfile SUMMARY="Central Backend for Odigos" DESCRIPTION="Central Backend is the core component of Odigos Central, responsible for managing the data flow from the cluster to the ui." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-central-ui
build-central-ui:
	$(MAKE) build-image/central-ui DOCKERFILE=central-ui/$(DOCKERFILE) BUILD_DIR=central-ui SUMMARY="Central UI for Odigos" DESCRIPTION="Central UI is the web interface for Odigos Central, providing a user-friendly way to manage and visualize the data collected by Odigos." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: build-central-proxy
build-central-proxy:
	$(MAKE) build-image/central-proxy DOCKERFILE=central-proxy/Dockerfile SUMMARY="Central Proxy for Odigos" DESCRIPTION="Central Proxy is handle the communication between the cluster that Odigos is running in and the Odigos Central." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

############################################
# Cloud connectors (platformType = connector)
#
# Go connector images compile inside Docker (no host build/ artifacts). GITHUB_TOKEN is required for
# private module download.
############################################

.PHONY: build-connector-runtime
build-connector-runtime:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker build $(TARGET_FLAG) \
		-t $(ORG)/odigos-enterprise-connector-runtime$(IMG_SUFFIX):$(TAG) \
		-f connector-runtime/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		. $(NO_CACHE)

# connector-opamp-gateway is the public OpAMP endpoint the managed-Collector fleet connects back to.
.PHONY: build-connector-opamp-gateway
build-connector-opamp-gateway:
	$(MAKE) build-image/connector-opamp-gateway DOCKERFILE=connector-opamp-gateway/Dockerfile SUMMARY="OpAMP gateway for Odigos" DESCRIPTION="Public OpAMP endpoint that authenticates managed Collectors and proxies each session to the connector owning its cloud account." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: push-connector-opamp-gateway
push-connector-opamp-gateway:
	$(MAKE) push-image/connector-opamp-gateway DOCKERFILE=connector-opamp-gateway/Dockerfile SUMMARY="OpAMP gateway for Odigos" DESCRIPTION="Public OpAMP endpoint that authenticates managed Collectors and proxies each session to the connector owning its cloud account." TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)

# The Lambda layer bundle and the Fargate agents bundle are no longer built here. Both are released by
# odigos-io/aws-connector-artifacts, which also publishes the SAME layer zips as public Lambda layer
# versions and as Serverless Application Repository applications — a customer deploying the layer
# themselves and a customer whose functions the connector instruments have to get identical bytes, and
# that only holds if there is one build.
#
# This connector consumes them by tag:
#   - LAMBDA_AGENTS_IMAGE (build arg, defaulted in connectors/providers/aws/Dockerfile) supplies the layer
#     zips the connector publishes into customer accounts.
#   - config.DefaultFargateAgentsImage names the bundle instrumented Fargate tasks pull at runtime. It was
#     never pulled by this build or by the kind cluster, which is why there was no load-to-kind target for
#     it and why nothing here replaces build-fargate-agents.
#
# ARTIFACTS_VERSION is the single place the artifacts release this connector is built against is written
# down. Both pins derive from it, and pkg/config's TestArtifactVersions fails if they drift — the two
# would otherwise be a Dockerfile ARG and a Go constant that nothing forces to agree, and a connector
# shipping v0.3.0 layers while staging v0.1.0 Fargate agents is not a state anyone would notice.
#
# Bumping the artifacts version is: edit that file, run the test, open a PR.
ARTIFACTS_VERSION := $(shell cat connectors/providers/aws/ARTIFACTS_VERSION)
LAMBDA_AGENTS_REGISTRY ?= enterprise-registry.odigos.io

# Override to build against an unreleased layer bundle (the version check does not apply to an override,
# which is the point of having one):
#   make build-connector-aws LAMBDA_AGENTS_IMAGE=my-registry/odigos-lambda-agents:dev
LAMBDA_AGENTS_IMAGE ?= $(LAMBDA_AGENTS_REGISTRY)/odigos-lambda-agents:$(ARTIFACTS_VERSION)

.PHONY: build-connector-aws
build-connector-aws:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker build $(TARGET_FLAG) \
		-t $(ORG)/odigos-enterprise-connector-aws$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/aws/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		--build-arg LAMBDA_AGENTS_IMAGE=$(LAMBDA_AGENTS_IMAGE) \
		. $(NO_CACHE)

.PHONY: build-connector-gcp
build-connector-gcp:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker build $(TARGET_FLAG) \
		-t $(ORG)/odigos-enterprise-connector-gcp$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/gcp/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		. $(NO_CACHE)

.PHONY: build-connector-azure
build-connector-azure:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker build $(TARGET_FLAG) \
		-t $(ORG)/odigos-enterprise-connector-azure$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/azure/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		. $(NO_CACHE)

.PHONY: build-connector-mulesoft
build-connector-mulesoft:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker build $(TARGET_FLAG) \
		-t $(ORG)/odigos-enterprise-connector-mulesoft$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/mulesoft/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		. $(NO_CACHE)

# connector-postgres is a plain Docker image (PostgreSQL + goose + migrations); it needs no Go build
# or GITHUB_TOKEN, so it does not go through build-image/%.
.PHONY: build-connector-postgres
build-connector-postgres:
	docker build -t $(ORG)/odigos-enterprise-connector-postgres$(IMG_SUFFIX):$(TAG) connector-postgres -f connector-postgres/Dockerfile $(NO_CACHE)

# Validate the connector-postgres goose migrations parse (up/down annotations well-formed, versions
# unique/ordered). Requires Docker only — no local goose/Postgres install.
#
# The Go image and GOOSE_VERSION mirror .github/workflows/connector-postgres-migrations.yml, which runs
# this validate step plus a full up -> down -> up round trip against a real Postgres (the round trip needs
# a database, so it lives only in CI). Keep both pins in sync with that workflow.
#
# goose is pinned rather than @latest on purpose: @latest tracks a release whose minimum Go version moves
# independently of this image, which silently breaks the target (v3.28.x requires Go >= 1.26).
GOOSE_VERSION ?= v3.28.0
.PHONY: validate-db-migrations
validate-db-migrations:
	docker run --rm -v "$(CURDIR)/connector-postgres/migrations:/migrations" golang:1.27.4 \
		sh -c 'CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION) && goose -dir /migrations validate'

.PHONY: build-connector-runtime-dev
build-connector-runtime-dev: build-connector-runtime

.PHONY: build-connector-opamp-gateway-dev
build-connector-opamp-gateway-dev: build-connector-opamp-gateway

.PHONY: build-connector-aws-dev
build-connector-aws-dev: build-connector-aws

.PHONY: build-connector-gcp-dev
build-connector-gcp-dev: build-connector-gcp

.PHONY: build-connector-azure-dev
build-connector-azure-dev: build-connector-azure

.PHONY: build-connector-mulesoft-dev
build-connector-mulesoft-dev: build-connector-mulesoft

# Aggregate builds are plain prerequisite fan-outs (no sub-make), so parallel make builds the
# independent images concurrently — e.g. `make -j$(nproc) build-connector-images`. The images share no
# build ordering; ORG/TAG/IMG_SUFFIX are in scope for every prerequisite, so no explicit pass-through is
# needed. Tip: add -O (output-sync) to keep concurrent docker build logs grouped per target.
.PHONY: build-connector-images
build-connector-images: build-connector-postgres build-connector-runtime build-connector-opamp-gateway build-connector-aws build-connector-gcp build-connector-azure build-connector-mulesoft

# Build every image the cloud-connectors dev stack needs: the central images plus postgres and the
# connector services (all built inside Docker). Parallel-safe like build-connector-images.
.PHONY: build-connector-stack
build-connector-stack: build-central-backend build-central-ui build-connector-images

.PHONY: load-connector-stack-to-kind
load-connector-stack-to-kind:
	make load-to-kind-central-backend load-to-kind-central-ui load-to-kind-connector-postgres load-to-kind-connector-runtime load-to-kind-connector-opamp-gateway load-to-kind-connector-aws load-to-kind-connector-gcp load-to-kind-connector-azure load-to-kind-connector-mulesoft ORG=$(ORG) TAG=$(TAG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: push-connector-runtime
push-connector-runtime:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker buildx build $(TARGET_FLAG) \
		--platform linux/amd64,linux/arm64/v8 \
		-t $(ORG)/odigos-enterprise-connector-runtime$(IMG_SUFFIX):$(TAG) \
		-f connector-runtime/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		--push \
		. $(NO_CACHE)

.PHONY: push-connector-aws
push-connector-aws:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker buildx build $(TARGET_FLAG) \
		--platform linux/amd64,linux/arm64/v8 \
		-t $(ORG)/odigos-enterprise-connector-aws$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/aws/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		--build-arg LAMBDA_AGENTS_IMAGE=$(LAMBDA_AGENTS_IMAGE) \
		--push \
		. $(NO_CACHE)

.PHONY: push-connector-gcp
push-connector-gcp:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker buildx build $(TARGET_FLAG) \
		--platform linux/amd64,linux/arm64/v8 \
		-t $(ORG)/odigos-enterprise-connector-gcp$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/gcp/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		--push \
		. $(NO_CACHE)

.PHONY: push-connector-azure
push-connector-azure:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker buildx build $(TARGET_FLAG) \
		--platform linux/amd64,linux/arm64/v8 \
		-t $(ORG)/odigos-enterprise-connector-azure$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/azure/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		--push \
		. $(NO_CACHE)

.PHONY: push-connector-mulesoft
push-connector-mulesoft:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "❌ GITHUB_TOKEN is not set. Export a GitHub PAT with access to odigos-io private repos."; \
		exit 1; \
	fi
	docker buildx build $(TARGET_FLAG) \
		--platform linux/amd64,linux/arm64/v8 \
		-t $(ORG)/odigos-enterprise-connector-mulesoft$(IMG_SUFFIX):$(TAG) \
		-f connectors/providers/mulesoft/Dockerfile \
		--secret id=github_token,env=GITHUB_TOKEN \
		--build-arg LD_FLAGS="$(LD_FLAGS)" \
		--push \
		. $(NO_CACHE)

.PHONY: push-connector-postgres
push-connector-postgres:
	docker buildx build --push --platform linux/amd64,linux/arm64/v8 -t $(ORG)/odigos-enterprise-connector-postgres$(IMG_SUFFIX):$(TAG) connector-postgres -f connector-postgres/Dockerfile

.PHONY: load-connectors-to-kind
load-connectors-to-kind:
	make load-to-kind-connector-postgres load-to-kind-connector-runtime load-to-kind-connector-aws load-to-kind-connector-gcp load-to-kind-connector-azure load-to-kind-connector-mulesoft ORG=$(ORG) TAG=$(TAG) IMG_SUFFIX=$(IMG_SUFFIX)

.PHONY: restart-connector-runtime
restart-connector-runtime:
	kubectl rollout restart deployment connector-runtime -n odigos-central || true

.PHONY: restart-connector-postgres
restart-connector-postgres:
	kubectl rollout restart deployment odigos-connector-postgres -n odigos-central || true

# Restart connector-aws workload pods (created by connector-runtime from OdigosCloudConnector CRs).
# Set CONNECTOR=<cr-name> to restart one connector; defaults to all AWS connectors in the namespace.
.PHONY: restart-connector-aws
restart-connector-aws:
	@selector="odigos.io/provider=aws"; \
	if [ -n "$(CONNECTOR)" ]; then selector="odigos.io/cloud-connector=$(CONNECTOR)"; fi; \
	echo "restarting connector deployments in $(ODIGOS_CENTRAL_NS) ($$selector)"; \
	kubectl rollout restart deployment -n $(ODIGOS_CENTRAL_NS) -l "$$selector"

# Port-forward a connector pod's odictl CLI WebSocket (/ws) to localhost:23678, then drive it from
# odictl (~/code/vm-agent/cmd/odictl) with no flags: `cd ~/code/vm-agent/cmd/odictl && go run .`.
# Set CONNECTOR=<deployment/pod> (defaults to the first connector pod in the namespace).
CONNECTOR ?=
.PHONY: port-forward-connector-aws
port-forward-connector-aws:
	@target="$(CONNECTOR)"; \
	if [ -z "$$target" ]; then \
		target=$$(kubectl -n $(ODIGOS_CENTRAL_NS) get pods -l odigos.io/provider=aws -o jsonpath='{.items[0].metadata.name}'); \
	fi; \
	echo "port-forwarding $$target 23678:23678 (run odictl with no flags to connect)"; \
	kubectl -n $(ODIGOS_CENTRAL_NS) port-forward "$$target" 23678:23678

# Restart connector-gcp workload pods (created by connector-runtime from OdigosCloudConnector CRs).
# Set CONNECTOR=<cr-name> to restart one connector; defaults to all GCP connectors in the namespace.
.PHONY: restart-connector-gcp
restart-connector-gcp:
	@selector="odigos.io/provider=gcp"; \
	if [ -n "$(CONNECTOR)" ]; then selector="odigos.io/cloud-connector=$(CONNECTOR)"; fi; \
	echo "restarting connector deployments in $(ODIGOS_CENTRAL_NS) ($$selector)"; \
	kubectl rollout restart deployment -n $(ODIGOS_CENTRAL_NS) -l "$$selector"

.PHONY: port-forward-connector-gcp
port-forward-connector-gcp:
	@target="$(CONNECTOR)"; \
	if [ -z "$$target" ]; then \
		target=$$(kubectl -n $(ODIGOS_CENTRAL_NS) get pods -l odigos.io/provider=gcp -o jsonpath='{.items[0].metadata.name}'); \
	fi; \
	echo "port-forwarding $$target 23678:23678 (run odictl with no flags to connect)"; \
	kubectl -n $(ODIGOS_CENTRAL_NS) port-forward "$$target" 23678:23678

# Restart connector-azure workload pods (created by connector-runtime from OdigosCloudConnector CRs).
# Set CONNECTOR=<cr-name> to restart one connector; defaults to all Azure connectors in the namespace.
.PHONY: restart-connector-azure
restart-connector-azure:
	@selector="odigos.io/provider=azure"; \
	if [ -n "$(CONNECTOR)" ]; then selector="odigos.io/cloud-connector=$(CONNECTOR)"; fi; \
	echo "restarting connector deployments in $(ODIGOS_CENTRAL_NS) ($$selector)"; \
	kubectl rollout restart deployment -n $(ODIGOS_CENTRAL_NS) -l "$$selector"

.PHONY: port-forward-connector-azure
port-forward-connector-azure:
	@target="$(CONNECTOR)"; \
	if [ -z "$$target" ]; then \
		target=$$(kubectl -n $(ODIGOS_CENTRAL_NS) get pods -l odigos.io/provider=azure -o jsonpath='{.items[0].metadata.name}'); \
	fi; \
	echo "port-forwarding $$target 23678:23678 (run odictl with no flags to connect)"; \
	kubectl -n $(ODIGOS_CENTRAL_NS) port-forward "$$target" 23678:23678

# Restart connector-mulesoft workload pods (created by connector-runtime from OdigosCloudConnector CRs).
# Set CONNECTOR=<cr-name> to restart one connector; defaults to all MuleSoft connectors in the namespace.
.PHONY: restart-connector-mulesoft
restart-connector-mulesoft:
	@selector="odigos.io/provider=mulesoft"; \
	if [ -n "$(CONNECTOR)" ]; then selector="odigos.io/cloud-connector=$(CONNECTOR)"; fi; \
	echo "restarting connector deployments in $(ODIGOS_CENTRAL_NS) ($$selector)"; \
	kubectl rollout restart deployment -n $(ODIGOS_CENTRAL_NS) -l "$$selector"

.PHONY: port-forward-connector-mulesoft
port-forward-connector-mulesoft:
	@target="$(CONNECTOR)"; \
	if [ -z "$$target" ]; then \
		target=$$(kubectl -n $(ODIGOS_CENTRAL_NS) get pods -l odigos.io/provider=mulesoft -o jsonpath='{.items[0].metadata.name}'); \
	fi; \
	echo "port-forwarding $$target 23678:23678 (run odictl with no flags to connect)"; \
	kubectl -n $(ODIGOS_CENTRAL_NS) port-forward "$$target" 23678:23678

# One-command dev stack for cloud connectors, entirely via Helm: builds every required image, loads
# them into kind, and installs the odigos-central chart with central-ui + connector-runtime + Postgres
# (cloudConnectors enabled). Requires GITHUB_TOKEN (central image builds) and ONPREM_TOKEN (enterprise).
# Override CENTRAL_CHART to point at your odigos-central chart checkout.
# Set MANAGED_COLLECTOR_ENDPOINT to the public tunnel URL (with or without /v1/opamp) to enable
# managed Collectors and deploy the OpAMP gateway. Route /v1/opamp and /v1/metrics to its Service.
CENTRAL_CHART ?= helm/odigos-central
ODIGOS_CENTRAL_NS ?= odigos-central
KIND_CLUSTER ?= kind
ONPREM_TOKEN ?=
# Managed Collectors stay OFF unless you give the OpAMP gateway a publicly reachable address. A
# Collector runs in the customer's cloud account and dials Central, so a kind-local Service address is
# not reachable from it and there is no default that is right for an unknown installation — the
# connector refuses to deploy a Collector rather than point one at somebody else's endpoint.
# In a dev loop this is an ngrok/tailscale/cloudflared URL in front of the gateway Service:
#   make connector-dev-up TAG=dev ONPREM_TOKEN=$$ODIGOS_ONPREM_TOKEN \
#     MANAGED_COLLECTOR_ENDPOINT=wss://abc123.ngrok.app/v1/opamp
MANAGED_COLLECTOR_ENDPOINT ?=
ODIGOS_HELM_REPO ?= https://odigos-io.github.io/odigos/
ODIGOS_CENTRAL_CHART ?= odigos/odigos-central

.PHONY: ensure-kind
ensure-kind:
	@kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)" || { echo "creating kind cluster '$(KIND_CLUSTER)'..."; kind create cluster --name $(KIND_CLUSTER); }

.PHONY: connector-dev-up
connector-dev-up: ensure-kind
	@test -n "$(ONPREM_TOKEN)" || { echo "❌ Set ONPREM_TOKEN=<your odigos enterprise token> (and GITHUB_TOKEN for central image builds)."; exit 1; }
	make build-connector-stack -j$(nproc) TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	make load-connector-stack-to-kind -j$(nproc) TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX)
	@managed_endpoint="$(MANAGED_COLLECTOR_ENDPOINT)"; \
		if [ -n "$$managed_endpoint" ]; then \
			case "$$managed_endpoint" in \
				*/v1/opamp) ;; \
				*/) managed_endpoint="$${managed_endpoint}v1/opamp" ;; \
				*) managed_endpoint="$$managed_endpoint/v1/opamp" ;; \
			esac; \
		fi; \
		helm upgrade --install odigos-central $(CENTRAL_CHART) -n $(ODIGOS_CENTRAL_NS) --create-namespace \
			--set image.tag=$(TAG) --set onPremToken=$(ONPREM_TOKEN) --set cloudConnectors.enabled=true \
			--set cloudConnectors.managedCollectors.enabled=$(if $(strip $(MANAGED_COLLECTOR_ENDPOINT)),true,false) \
			--set-literal cloudConnectors.managedCollectors.externalEndpoint="$$managed_endpoint"
	@echo "✅ Stack up in namespace $(ODIGOS_CENTRAL_NS) (central-ui + central-backend + connector-runtime + postgres$(if $(strip $(MANAGED_COLLECTOR_ENDPOINT)), + OpAMP gateway,))."
	@echo "   central-ui:        kubectl -n $(ODIGOS_CENTRAL_NS) port-forward svc/central-ui 3000:3000  # http://localhost:3000"
	@echo "   apply a connector: kubectl apply -f <your-OdigosCloudConnector>.yaml -n $(ODIGOS_CENTRAL_NS)"
	@echo "   discovered data:   kubectl -n $(ODIGOS_CENTRAL_NS) port-forward svc/<connector-workload> 9090:8080  # then POST /graphql"
ifeq ($(strip $(MANAGED_COLLECTOR_ENDPOINT)),)
	@echo "   managed Collectors: OFF (no MANAGED_COLLECTOR_ENDPOINT). Re-run with a publicly reachable"
	@echo "                       wss:// address in front of the gateway to enable them."
else
	@echo "   opamp gateway:     $(MANAGED_COLLECTOR_ENDPOINT) -> svc/connector-opamp-gateway:8080"
	@echo "   grant a connector: kubectl patch odigoscloudconnector <name> -n $(ODIGOS_CENTRAL_NS) --type=merge \\"
	@echo "                        -p '{\"spec\":{\"collectors\":[{\"type\":\"aws.fargate-collector\",\"management\":true,\"deployment\":true}]}}'"
endif

# Install the cloud-connectors stack from a released odigos-central chart rather than locally built
# images. VERSION is both the released chart version and the enterprise image tag (for example,
# make connector-release-up VERSION=v1.0.0 ONPREM_TOKEN=$$ODIGOS_ONPREM_TOKEN).
# Set ORG to use a mirrored enterprise image registry.
.PHONY: connector-release-up
connector-release-up: ensure-kind
	@test -n "$(VERSION)" || { echo "❌ Set VERSION=<released Odigos version>."; exit 1; }
	@test -n "$(ONPREM_TOKEN)" || { echo "❌ Set ONPREM_TOKEN=<your odigos enterprise token>."; exit 1; }
	helm repo add odigos $(ODIGOS_HELM_REPO) --force-update
	helm repo update odigos
	@managed_endpoint="$(MANAGED_COLLECTOR_ENDPOINT)"; \
		if [ -n "$$managed_endpoint" ]; then \
			case "$$managed_endpoint" in \
				*/v1/opamp) ;; \
				*/) managed_endpoint="$${managed_endpoint}v1/opamp" ;; \
				*) managed_endpoint="$$managed_endpoint/v1/opamp" ;; \
			esac; \
		fi; \
		helm upgrade --install odigos-central $(ODIGOS_CENTRAL_CHART) --version $(VERSION) -n $(ODIGOS_CENTRAL_NS) --create-namespace \
			--set imagePrefix=$(ORG) --set image.tag=$(VERSION) --set onPremToken=$(ONPREM_TOKEN) --set cloudConnectors.enabled=true \
			--set cloudConnectors.managedCollectors.enabled=$(if $(strip $(MANAGED_COLLECTOR_ENDPOINT)),true,false) \
			--set-literal cloudConnectors.managedCollectors.externalEndpoint="$$managed_endpoint"
	@echo "✅ Released stack $(VERSION) installed in namespace $(ODIGOS_CENTRAL_NS) (central-ui + central-backend + connector-runtime + postgres$(if $(strip $(MANAGED_COLLECTOR_ENDPOINT)), + OpAMP gateway,))."
	@echo "   central-ui:        kubectl -n $(ODIGOS_CENTRAL_NS) port-forward svc/central-ui 3000:3000  # http://localhost:3000"
	@echo "   apply a connector: kubectl apply -f <your-OdigosCloudConnector>.yaml -n $(ODIGOS_CENTRAL_NS)"
	@echo "   discovered data:   kubectl -n $(ODIGOS_CENTRAL_NS) port-forward svc/<connector-workload> 9090:8080  # then POST /graphql"
ifeq ($(strip $(MANAGED_COLLECTOR_ENDPOINT)),)
	@echo "   managed Collectors: OFF (no MANAGED_COLLECTOR_ENDPOINT). Re-run with a publicly reachable"
	@echo "                       wss:// address in front of the gateway to enable them."
else
	@echo "   opamp gateway:     $(MANAGED_COLLECTOR_ENDPOINT) -> svc/connector-opamp-gateway:8080"
	@echo "   grant a connector: kubectl patch odigoscloudconnector <name> -n $(ODIGOS_CENTRAL_NS) --type=merge \\"
	@echo "                        -p '{\"spec\":{\"collectors\":[{\"type\":\"aws.fargate-collector\",\"management\":true,\"deployment\":true}]}}'"
endif

.PHONY: connector-dev-down
connector-dev-down:
	kubectl delete odigoscloudconnectors --all -n $(ODIGOS_CENTRAL_NS) --ignore-not-found || true
	helm upgrade --install odigos-central $(CENTRAL_CHART) -n $(ODIGOS_CENTRAL_NS) \
		--set image.tag=$(TAG) --set onPremToken=$(ONPREM_TOKEN) --set cloudConnectors.enabled=false
	@echo "✅ connectors disabled (PostgreSQL PVC retained; delete it manually to wipe state)."

.PHONY: build-central-images
build-central-images:
	make build-central-backend build-central-ui build-central-proxy TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) DOCKERFILE=$(DOCKERFILE)

.PHONY: load-central-to-kind
load-central-to-kind:
	make load-to-kind-central-backend load-to-kind-central-ui load-to-kind-central-proxy ORG=$(ORG) TAG=$(TAG) IMG_SUFFIX=$(IMG_SUFFIX)

# Local e2e prep (mirrors odigos `make dev-tests-setup`). Does NOT create the kind cluster —

.PHONY: restart-central-backend
restart-central-backend:
	kubectl rollout restart deployment central-backend -n odigos-central || true

.PHONY: restart-central-ui
restart-central-ui:
	kubectl rollout restart deployment central-ui -n odigos-central || true

.PHONY: restart-central-proxy
restart-central-proxy:
	kubectl rollout restart deployment central-proxy -n odigos-system || true

.PHONY: restart-central
restart-central:
	make restart-central-backend && make restart-central-ui && make restart-central-proxy

.PHONY: deploy-central-backend
deploy-central-backend:
	make build-central-backend TAG=$(TAG) && make load-to-kind-central-backend TAG=$(TAG) && make restart-central-backend

.PHONY: deploy-central-ui
deploy-central-ui:
	make build-central-ui TAG=$(TAG) && make load-to-kind-central-ui TAG=$(TAG) && make restart-central-ui

.PHONY: deploy-central-proxy
deploy-central-proxy:
	make build-central-proxy TAG=$(TAG) && make load-to-kind-central-proxy TAG=$(TAG) && make restart-central-proxy

.PHONY: deploy-connector-aws
deploy-connector-aws:
	make build-connector-aws TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make load-to-kind-connector-aws TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make restart-connector-aws

.PHONY: deploy-connector-gcp
deploy-connector-gcp:
	make build-connector-gcp TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make load-to-kind-connector-gcp TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make restart-connector-gcp

.PHONY: deploy-connector-azure
deploy-connector-azure:
	make build-connector-azure TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make load-to-kind-connector-azure TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make restart-connector-azure

.PHONY: deploy-connector-mulesoft
deploy-connector-mulesoft:
	make build-connector-mulesoft TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make load-to-kind-connector-mulesoft TAG=$(TAG) ORG=$(ORG) IMG_SUFFIX=$(IMG_SUFFIX) && \
	make restart-connector-mulesoft

.PHONY: deploy-central
deploy-central:
	make build-central-images TAG=$(TAG) && make load-central-to-kind TAG=$(TAG) && make restart-central
