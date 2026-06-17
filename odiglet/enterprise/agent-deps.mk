# Instrumentation-agent version bumps. Kept out of the main Makefile so the agent-update CI
# (ci-core/apply-agent-version-update) can drive it via `make -f agent-deps.mk upgrade-agent` without touching
# the build/generate targets. Run targets here with `make -f agent-deps.mk <target>`.

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
			$(MAKE) -f $(MK) upgrade-odiglet-python-community-version AGENT_VERSION=$(AGENT_VERSION) ;; \
		php|php-community) \
			$(MAKE) -f $(MK) upgrade-odiglet-agent-version AGENT_DISTRO=php-community AGENT_VERSION=$(AGENT_VERSION) ;; \
		ruby|ruby-community) \
			$(MAKE) -f $(MK) upgrade-odiglet-agent-version AGENT_DISTRO=ruby-community AGENT_VERSION=$(AGENT_VERSION) ;; \
		java-ebpf|java-enterprise|java-ebpf-instrumentations) \
			$(MAKE) -f $(MK) upgrade-java-ebpf-agents-version JAVA_EBPF_AGENTS_VERSION=$(AGENT_VERSION) ;; \
		nodejs-enterprise|nodejs|nodejs-ebpf) \
			$(MAKE) -f $(MK) upgrade-nodejs-ebpf-agents-version NODEJS_EBPF_AGENTS_VERSION=$(AGENT_VERSION) ;; \
		python-ebpf) \
			$(MAKE) -f $(MK) upgrade-python-ebpf-agents-version PYTHON_EBPF_AGENTS_VERSION=$(AGENT_VERSION) ;; \
		golang) \
			$(MAKE) -f $(MK) upgrade-golang-agents-version GOLANG_AGENTS_VERSION=$(AGENT_VERSION) ;; \
		*) \
			echo "Instrumentation agent '$(INSTRUMENTATION_AGENT)' is not consumed by odigos-enterprise; nothing to do." ;; \
	esac

.PHONY: upgrade-java-ebpf-agents-version
upgrade-java-ebpf-agents-version:
	@if [ -z "$(JAVA_EBPF_AGENTS_VERSION)" ]; then \
		echo "ERROR: JAVA_EBPF_AGENTS_VERSION parameter is required. Usage: make -f agent-deps.mk upgrade-java-ebpf-agents-version JAVA_EBPF_AGENTS_VERSION=v0.10.13"; \
		exit 1; \
	fi
	@echo "Updating java ebpf agents version to $(JAVA_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+|java-ebpf-instrumentations:$(JAVA_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|java-enterprise:[0-9A-Za-z._-]+|java-enterprise:$(JAVA_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+|java-ebpf-instrumentations:$(JAVA_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
		sed -i '' -E 's|java-enterprise:[0-9A-Za-z._-]+|java-enterprise:$(JAVA_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+|java-ebpf-instrumentations:$(JAVA_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i -E 's|java-enterprise:[0-9A-Za-z._-]+|java-enterprise:$(JAVA_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i -E 's|java-ebpf-instrumentations:[0-9A-Za-z._-]+|java-ebpf-instrumentations:$(JAVA_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
		sed -i -E 's|java-enterprise:[0-9A-Za-z._-]+|java-enterprise:$(JAVA_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
	fi
	@echo "Updating Go module versions using go get"
	@go get github.com/odigos-io/ebpf-java-instrumentation/v2@$(JAVA_EBPF_AGENTS_VERSION)
	@go get github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/v2@$(JAVA_EBPF_AGENTS_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated java ebpf agents version to $(JAVA_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

.PHONY: upgrade-nodejs-ebpf-agents-version
upgrade-nodejs-ebpf-agents-version:
	@if [ -z "$(NODEJS_EBPF_AGENTS_VERSION)" ]; then \
		echo "ERROR: NODEJS_EBPF_AGENTS_VERSION parameter is required. Usage: make -f agent-deps.mk upgrade-nodejs-ebpf-agents-version NODEJS_EBPF_AGENTS_VERSION=v0.0.9"; \
		exit 1; \
	fi
	@echo "Updating nodejs ebpf agents version to $(NODEJS_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|nodejs-enterprise:[0-9A-Za-z._-]+|nodejs-enterprise:$(NODEJS_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|nodejs-enterprise:[0-9A-Za-z._-]+|nodejs-enterprise:$(NODEJS_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|nodejs-enterprise:[0-9A-Za-z._-]+|nodejs-enterprise:$(NODEJS_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i -E 's|nodejs-enterprise:[0-9A-Za-z._-]+|nodejs-enterprise:$(NODEJS_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
	fi
	@echo "Updating Go module versions using go get"
	@go get github.com/odigos-io/ebpf-nodejs-instrumentation@$(NODEJS_EBPF_AGENTS_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated nodejs ebpf agents version to $(NODEJS_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

# Generic image-tag bump for image-only agents (php, ruby, and any future image-only distro).
# Pass AGENT_DISTRO matching the image name in the dockerfiles (e.g. php-community).
.PHONY: upgrade-odiglet-agent-version
upgrade-odiglet-agent-version:
	@if [ -z "$(AGENT_DISTRO)" ] || [ -z "$(AGENT_VERSION)" ]; then \
		echo 'ERROR: AGENT_DISTRO and AGENT_VERSION are required. Example: make -f agent-deps.mk upgrade-odiglet-agent-version AGENT_DISTRO=php-community AGENT_VERSION=v0.3.2'; \
		exit 1; \
	fi
	@echo "Updating $(AGENT_DISTRO) agent image tag to $(AGENT_VERSION) in Dockerfile and debug.Dockerfile"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+|$(AGENT_DISTRO):$(AGENT_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+|$(AGENT_DISTRO):$(AGENT_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+|$(AGENT_DISTRO):$(AGENT_VERSION)|g' Dockerfile; \
		sed -i -E 's|$(AGENT_DISTRO):[0-9A-Za-z._-]+|$(AGENT_DISTRO):$(AGENT_VERSION)|g' debug.Dockerfile; \
	fi

# python-community has two image tags pinned in the dockerfiles, this upgrades the specific agent version that needs to be updated and not python3.8
.PHONY: upgrade-odiglet-python-community-version
upgrade-odiglet-python-community-version:
	@if [ -z "$(AGENT_VERSION)" ]; then \
		echo 'ERROR: AGENT_VERSION is required. Example: make -f agent-deps.mk upgrade-odiglet-python-community-version AGENT_VERSION=v0.1.66'; \
		exit 1; \
	fi
	@echo "Updating python-community agent image tag to $(AGENT_VERSION) in Dockerfile and debug.Dockerfile (preserving -py3.8 pin)"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|python-community:[0-9A-Za-z._-]+( +/instrumentations/python )|python-community:$(AGENT_VERSION)\1|g' Dockerfile; \
		sed -i '' -E 's|python-community:[0-9A-Za-z._-]+( +/instrumentations/python )|python-community:$(AGENT_VERSION)\1|g' debug.Dockerfile; \
	else \
		sed -i -E 's|python-community:[0-9A-Za-z._-]+( +/instrumentations/python )|python-community:$(AGENT_VERSION)\1|g' Dockerfile; \
		sed -i -E 's|python-community:[0-9A-Za-z._-]+( +/instrumentations/python )|python-community:$(AGENT_VERSION)\1|g' debug.Dockerfile; \
	fi

# python-ebpf ships a docker image (python-ebpf) AND a go module (ebpf-generic-instrumentation),
# pinned at the same version.
.PHONY: upgrade-python-ebpf-agents-version
upgrade-python-ebpf-agents-version:
	@if [ -z "$(PYTHON_EBPF_AGENTS_VERSION)" ]; then \
		echo "ERROR: PYTHON_EBPF_AGENTS_VERSION parameter is required. Usage: make -f agent-deps.mk upgrade-python-ebpf-agents-version PYTHON_EBPF_AGENTS_VERSION=v0.0.12"; \
		exit 1; \
	fi
	@echo "Updating python ebpf agents version to $(PYTHON_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"
	@if [ "$(shell uname)" = "Darwin" ]; then \
		sed -i '' -E 's|python-ebpf:[0-9A-Za-z._-]+|python-ebpf:$(PYTHON_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i '' -E 's|python-ebpf:[0-9A-Za-z._-]+|python-ebpf:$(PYTHON_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
	else \
		sed -i -E 's|python-ebpf:[0-9A-Za-z._-]+|python-ebpf:$(PYTHON_EBPF_AGENTS_VERSION)|g' Dockerfile; \
		sed -i -E 's|python-ebpf:[0-9A-Za-z._-]+|python-ebpf:$(PYTHON_EBPF_AGENTS_VERSION)|g' debug.Dockerfile; \
	fi
	@echo "Updating Go module versions using go get"
	@go get github.com/odigos-io/ebpf-generic-instrumentation@$(PYTHON_EBPF_AGENTS_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated python ebpf agents version to $(PYTHON_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

# golang (enterprise-go-instrumentation) is consumed only as a go.mod replace of go.opentelemetry.io/auto.
.PHONY: upgrade-golang-agents-version
upgrade-golang-agents-version:
	@if [ -z "$(GOLANG_AGENTS_VERSION)" ]; then \
		echo "ERROR: GOLANG_AGENTS_VERSION parameter is required. Usage: make -f agent-deps.mk upgrade-golang-agents-version GOLANG_AGENTS_VERSION=v1.18.0"; \
		exit 1; \
	fi
	@echo "Updating golang (enterprise-go-instrumentation) version to $(GOLANG_AGENTS_VERSION) in go.mod"
	@go mod edit -replace go.opentelemetry.io/auto=github.com/odigos-io/enterprise-go-instrumentation@$(GOLANG_AGENTS_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated golang agents version to $(GOLANG_AGENTS_VERSION) in go.mod"
