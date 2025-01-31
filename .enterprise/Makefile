TAG ?= $(shell odigos version --cluster)
# These are used to point to different Dockerfiles (such as Dockerfile.rhel) and add -ubi9 to RHEL based images.
DOCKERFILE=Dockerfile
IMG_SUFFIX=

.PHONY: build-images
build-images:
	docker build -t keyval/odigos-enterprise-odiglet$(IMG_SUFFIX):$(TAG) . -f odiglet/$(DOCKERFILE) --secret id=github_token,env=GITHUB_TOKEN --build-arg ODIGOS_VERSION=$(TAG) --build-arg SUMMARY="Odiglet for Odigos" --build-arg DESCRIPTION="Odiglet is the core component of Odigos managing auto-instrumentation. This container requires a root user to run and manage eBPF programs." --build-arg VERSION=$(TAG) --build-arg RELEASE=$(TAG)

.PHONY: push-images
push-images:
	docker push keyval/odigos-enterprise-odiglet$(IMG_SUFFIX):$(TAG)

.PHONY: load-to-kind
load-to-kind:
	kind load docker-image keyval/odigos-enterprise-odiglet$(IMG_SUFFIX):$(TAG)

.PHONY: restart-odiglet
restart-odiglet:
	kubectl rollout restart daemonset odiglet -n odigos-system

.PHONY: deploy
deploy:
	make build-images TAG=$(TAG) && make load-to-kind TAG=$(TAG) && make restart-odiglet

.PHONY: debug-odiglet
debug-odiglet:
	docker build -t keyval/odigos-enterprise-odiglet:$(TAG) . -f odiglet/debug.Dockerfile --secret id=github_token,env=GITHUB_TOKEN
	kind load docker-image keyval/odigos-enterprise-odiglet:$(TAG)
	kubectl delete pod -n odigos-system -l app.kubernetes.io/name=odiglet
	kubectl wait --for=condition=ready pod -n odigos-system -l app.kubernetes.io/name=odiglet
	kubectl port-forward -n odigos-system daemonset/odiglet 2345:2345
