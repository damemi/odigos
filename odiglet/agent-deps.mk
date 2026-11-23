# Instrumentation-agent version bumps. Kept out of the main Makefile so the agent-update CI
# (ci-core/apply-agent-version-update) can drive it via `make -f agent-deps.mk upgrade-agent` without touching
# the build/generate targets. Run targets here with `make -f agent-deps.mk <target>`.
#
# Naming: every per-agent target is `upgrade-<distro>-version` and takes a single AGENT_VERSION
# (image-only distros also take AGENT_DISTRO). The dispatcher below maps a canonical
# INSTRUMENTATION_AGENT to the matching target.

# Path to THIS makefile, so the dispatcher can recurse into its own sub-targets.
MK := $(lastword $(MAKEFILE_LIST))

# Generic entrypoint used by CI. Maps a canonical INSTRUMENTATION_AGENT to the concrete target for this
# repo. Unknown categories are a no-op so a release can safely broadcast to all consumers.
.PHONY: upgrade-agent
upgrade-agent:
	@if [ -z "$(INSTRUMENTATION_AGENT)" ] || [ -z "$(AGENT_VERSION)" ]; then \
		echo 'ERROR: INSTRUMENTATION_AGENT and AGENT_VERSION are required. Example: make -f agent-deps.mk upgrade-agent INSTRUMENTATION_AGENT=java-ebpf AGENT_VERSION=v2.24.0'; \
		exit 1; \
	fi
	@category=$$(echo "$(INSTRUMENTATION_AGENT)" | tr '[:upper:]' '[:lower:]'); \
	case "$$category" in \
		python|python-community) \
			$(MAKE) -f $(MK) upgrade-python-community-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		php|php-community) \
			$(MAKE) -f $(MK) upgrade-image-agent-version AGENT_DISTRO=php-community AGENT_VERSION=$(AGENT_VERSION) ;; \
		ruby|ruby-community) \
			$(MAKE) -f $(MK) upgrade-image-agent-version AGENT_DISTRO=ruby-community AGENT_VERSION=$(AGENT_VERSION) ;; \
		ruby-legacy|ruby-community-legacy) \
			$(MAKE) -f $(MK) upgrade-image-agent-version AGENT_DISTRO=ruby-community-legacy AGENT_VERSION=$(AGENT_VERSION) ;; \
		java-ebpf|java-enterprise|java-ebpf-instrumentations) \
			$(MAKE) -f $(MK) upgrade-java-ebpf-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		nodejs-enterprise|nodejs|nodejs-ebpf) \
			$(MAKE) -f $(MK) upgrade-nodejs-ebpf-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		python-ebpf) \
			$(MAKE) -f $(MK) upgrade-python-ebpf-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		golang) \
			$(MAKE) -f $(MK) upgrade-golang-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		cpp) \
			$(MAKE) -f $(MK) upgrade-cpp-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		*) \
			echo "Instrumentation agent '$(INSTRUMENTATION_AGENT)' is not consumed by odiglet; nothing to do." ;; \
	esac

.PHONY: upgrade-java-ebpf-version
upgrade-java-ebpf-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo "ERROR: AGENT_VERSION is required. Usage: make -f agent-deps.mk upgrade-java-ebpf-version AGENT_VERSION=v0.10.13"; \
		exit 1; \
	fi
	@echo "Updating java ebpf agents version to $(AGENT_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-ebpf-instrumentations:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|java-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-enterprise:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-ebpf-instrumentations:$(AGENT_VERSION)|g' debug.Dockerfile; \
		sed -i '' -E 's|java-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-enterprise:$(AGENT_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-ebpf-instrumentations:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i -E 's|java-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-enterprise:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-ebpf-instrumentations:$(AGENT_VERSION)|g' debug.Dockerfile; \
		sed -i -E 's|java-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|java-enterprise:$(AGENT_VERSION)|g' debug.Dockerfile; \
	fi
	@$(MAKE) -f $(MK) pin-image-agent-digest AGENT_DISTRO=java-ebpf-instrumentations AGENT_VERSION=$(AGENT_VERSION) FILES="Dockerfile debug.Dockerfile"
	@$(MAKE) -f $(MK) pin-image-agent-digest AGENT_DISTRO=java-enterprise AGENT_VERSION=$(AGENT_VERSION) FILES="Dockerfile debug.Dockerfile"
	@echo "Updating Go module versions using go get"
	@go get github.com/odigos-io/ebpf-java-instrumentation/v2@$(AGENT_VERSION)
	@go get github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/v2@$(AGENT_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated java ebpf agents version to $(AGENT_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

.PHONY: upgrade-nodejs-ebpf-version
upgrade-nodejs-ebpf-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo "ERROR: AGENT_VERSION is required. Usage: make -f agent-deps.mk upgrade-nodejs-ebpf-version AGENT_VERSION=v0.0.9"; \
		exit 1; \
	fi
	@echo "Updating nodejs ebpf agents version to $(AGENT_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|nodejs-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|nodejs-enterprise:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|nodejs-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|nodejs-enterprise:$(AGENT_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|nodejs-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|nodejs-enterprise:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i -E 's|nodejs-enterprise:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|nodejs-enterprise:$(AGENT_VERSION)|g' debug.Dockerfile; \
	fi
	@$(MAKE) -f $(MK) pin-image-agent-digest AGENT_DISTRO=nodejs-enterprise AGENT_VERSION=$(AGENT_VERSION) FILES="Dockerfile debug.Dockerfile"
	@echo "Updating Go module versions using go get"
	@go get github.com/odigos-io/ebpf-nodejs-instrumentation@$(AGENT_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated nodejs ebpf agents version to $(AGENT_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

# python-ebpf ships a docker image (python-ebpf) AND a go module (ebpf-generic-instrumentation),
# pinned at the same version.
.PHONY: upgrade-python-ebpf-version
upgrade-python-ebpf-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo "ERROR: AGENT_VERSION is required. Usage: make -f agent-deps.mk upgrade-python-ebpf-version AGENT_VERSION=v0.0.12"; \
		exit 1; \
	fi
	@echo "Updating python ebpf agents version to $(AGENT_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|python-ebpf:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|python-ebpf:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|python-ebpf:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|python-ebpf:$(AGENT_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|python-ebpf:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|python-ebpf:$(AGENT_VERSION)|g' Dockerfile; \
		sed -i -E 's|python-ebpf:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|python-ebpf:$(AGENT_VERSION)|g' debug.Dockerfile; \
	fi
	@$(MAKE) -f $(MK) pin-image-agent-digest AGENT_DISTRO=python-ebpf AGENT_VERSION=$(AGENT_VERSION) FILES="Dockerfile debug.Dockerfile"
	@echo "Updating Go module versions using go get"
	@go get github.com/odigos-io/ebpf-generic-instrumentation@$(AGENT_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated python ebpf agents version to $(AGENT_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

# python-community has two image tags pinned in the dockerfiles, this upgrades the specific agent version that needs to be updated and not python3.8
.PHONY: upgrade-python-community-version
upgrade-python-community-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo 'ERROR: AGENT_VERSION is required. Example: make -f agent-deps.mk upgrade-python-community-version AGENT_VERSION=v0.1.66'; \
		exit 1; \
	fi
	@echo "Updating python-community agent image tag to $(AGENT_VERSION) in Dockerfile and debug.Dockerfile (preserving -py3.8 pin)"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|python-community:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?( +/instrumentations/python )|python-community:$(AGENT_VERSION)\2|g' Dockerfile; \
		sed -i '' -E 's|python-community:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?( +/instrumentations/python )|python-community:$(AGENT_VERSION)\2|g' debug.Dockerfile; \
	else \
		sed -i -E 's|python-community:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?( +/instrumentations/python )|python-community:$(AGENT_VERSION)\2|g' Dockerfile; \
		sed -i -E 's|python-community:[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?( +/instrumentations/python )|python-community:$(AGENT_VERSION)\2|g' debug.Dockerfile; \
	fi
	@$(MAKE) -f $(MK) pin-image-agent-digest AGENT_DISTRO=python-community AGENT_VERSION=$(AGENT_VERSION) FILES="Dockerfile debug.Dockerfile"

# Generic image-tag bump for image-only agents (php, ruby, and any future image-only distro).
# Pass AGENT_DISTRO matching the image name in the dockerfiles (e.g. php-community).
.PHONY: upgrade-image-agent-version
upgrade-image-agent-version:
	@if [ -z "$(AGENT_DISTRO)" ] || [ -z "$(AGENT_VERSION)" ]; then \
		echo 'ERROR: AGENT_DISTRO and AGENT_VERSION are required. Example: make -f agent-deps.mk upgrade-image-agent-version AGENT_DISTRO=php-community AGENT_VERSION=v0.3.2'; \
		exit 1; \
	fi
	@echo "Updating $(AGENT_DISTRO) agent image tag to $(AGENT_VERSION) in Dockerfile and debug.Dockerfile"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|$(AGENT_DISTRO):$(AGENT_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|$(AGENT_DISTRO):$(AGENT_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|$(AGENT_DISTRO):$(AGENT_VERSION)|g' Dockerfile; \
		sed -i -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+(@sha256:[0-9a-f]+)?|$(AGENT_DISTRO):$(AGENT_VERSION)|g' debug.Dockerfile; \
	fi
	@$(MAKE) -f $(MK) pin-image-agent-digest AGENT_DISTRO=$(AGENT_DISTRO) AGENT_VERSION=$(AGENT_VERSION) FILES="Dockerfile debug.Dockerfile"

# Internal: rewrite `<AGENT_DISTRO>:<AGENT_VERSION>` refs in FILES to `<tag>@<manifest digest>`.
# The dockerfiles pin agent images by digest (the digest is what the build resolves; the tag stays
# for readability), so every tag bump must re-resolve and rewrite the digest — a tag-only bump
# would silently keep the OLD image, because the digest wins over the tag.
.PHONY: pin-image-agent-digest
pin-image-agent-digest:
	@set -e; \
	ref="public.ecr.aws/odigos/agents/$(AGENT_DISTRO):$(AGENT_VERSION)"; \
	digest=$$(docker buildx imagetools inspect "$$ref" --format '{{.Manifest.Digest}}'); \
	if [ -z "$$digest" ]; then echo "ERROR: could not resolve digest for $$ref (does the tag exist?)"; exit 1; fi; \
	echo "Pinning $$ref@$$digest"; \
	for f in $(FILES); do \
		if [ "$$(uname)" = "Darwin" ]; then \
			sed -i '' -E "s|$(AGENT_DISTRO):$(AGENT_VERSION)([[:space:]])|$(AGENT_DISTRO):$(AGENT_VERSION)@$$digest\1|g" $$f; \
		else \
			sed -i -E "s|$(AGENT_DISTRO):$(AGENT_VERSION)([[:space:]])|$(AGENT_DISTRO):$(AGENT_VERSION)@$$digest\1|g" $$f; \
		fi; \
	done

.PHONY: upgrade-golang-version
upgrade-golang-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo "ERROR: AGENT_VERSION is required. Usage: make -f agent-deps.mk upgrade-golang-version AGENT_VERSION=v1.18.0"; \
		exit 1; \
	fi
	@echo "Updating golang (enterprise-go-instrumentation) version to $(AGENT_VERSION) in go.mod"
	@go get github.com/odigos-io/enterprise-go-instrumentation@$(AGENT_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated golang agents version to $(AGENT_VERSION) in go.mod"

# cpp (ebpf-core) is consumed as the github.com/odigos-io/ebpf-core go module in two modules:
# odiglet (where this makefile runs) and the sibling instrumentor module. Bump both to the same version.
.PHONY: upgrade-cpp-version
upgrade-cpp-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo "ERROR: AGENT_VERSION is required. Usage: make -f agent-deps.mk upgrade-cpp-version AGENT_VERSION=v0.14.0"; \
		exit 1; \
	fi
	@echo "Updating cpp (ebpf-core) go module version to $(AGENT_VERSION) in odiglet/go.mod"
	@go get github.com/odigos-io/ebpf-core@$(AGENT_VERSION)
	@go mod tidy
	@echo "Updating cpp (ebpf-core) go module version to $(AGENT_VERSION) in instrumentor/go.mod"
	@cd ../instrumentor && go get github.com/odigos-io/ebpf-core@$(AGENT_VERSION) && go mod tidy
	@echo "Successfully updated cpp (ebpf-core) version to $(AGENT_VERSION) in odiglet/go.mod and instrumentor/go.mod"
