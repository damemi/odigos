.PHONY: build-images
build-images:
	docker build -t keyval/odigos-enterprise-odiglet:$(TAG) . -f odiglet/Dockerfile --secret id=github_token,env=GITHUB_TOKEN

.PHONY: push-images
push-images:
	docker push keyval/odigos-enterprise-odiglet:$(TAG)

.PHONY: load-to-kind
load-to-kind:
	kind load docker-image keyval/odigos-enterprise-odiglet:$(TAG)

.PHONY: restart-odiglet
restart-odiglet:
	kubectl rollout restart daemonset odiglet -n odigos-system

.PHONY: deploy
deploy:
	make build-images TAG=$(TAG) && make load-to-kind TAG=$(TAG) && make restart-odiglet
