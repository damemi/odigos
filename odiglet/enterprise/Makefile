.PHONY: build-odiglet generate build-modular-docker build-final-docker

LIBRARIES := go.opentelemetry.io/auto \
			github.com/odigos-io/ebpf-java-instrumentation \
			github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension \
			github.com/odigos-io/ebpf-generic-instrumentation \
			github.com/odigos-io/ebpf-nodejs-instrumentation \
			github.com/odigos-io/mysql-instrumentation \
			github.com/odigos-io/runtime-detector

# For development, we can modify the libraries by adding a @<version/branch> suffix
# here, we make sure to remove the suffix to get the actual library name
CLEANED_LIBRARIES := $(patsubst %@%,%,$(foreach lib,$(LIBRARIES),$(if $(findstring @,$(lib)),$(firstword $(subst @, ,$(lib))),$(lib))))

# Convert spaces to commas to create a comma-separated list of libraries
space := $(subst ,, )
comma:= ,
GOPRIVATE := $(subst $(space),$(comma),$(CLEANED_LIBRARIES))
export GOPRIVATE

build-odiglet: generate
	go build -ldflags="$(LD_FLAGS)" -o odiglet ./cmd/enterprise

debug-build-odiglet: generate
	go build -gcflags "all=-N -l" -o odiglet ./cmd/enterprise

generate:
	go mod download
	@for lib in $(LIBRARIES); do \
		LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
		make -C "$$LIB_PATH" generate; \
	done

.PHONY: upgrade-java-ebpf-agents-version
upgrade-java-ebpf-agents-version:
	@if [ -z "$(JAVA_EBPF_AGENTS_VERSION)" ]; then \
		echo "ERROR: JAVA_EBPF_AGENTS_VERSION parameter is required. Usage: make upgrade-java-ebpf-agents-version JAVA_EBPF_AGENTS_VERSION=v0.10.13"; \
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
	@go get github.com/odigos-io/ebpf-java-instrumentation@$(JAVA_EBPF_AGENTS_VERSION)
	@go get github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension@$(JAVA_EBPF_AGENTS_VERSION)
	@echo "Running go mod tidy to clean up go.sum"
	@go mod tidy
	@echo "Successfully updated java ebpf agents version to $(JAVA_EBPF_AGENTS_VERSION) in Dockerfile, debug.Dockerfile, and go.mod"

.PHONY: upgrade-nodejs-ebpf-agents-version
upgrade-nodejs-ebpf-agents-version:
	@if [ -z "$(NODEJS_EBPF_AGENTS_VERSION)" ]; then \
		echo "ERROR: NODEJS_EBPF_AGENTS_VERSION parameter is required. Usage: make upgrade-nodejs-ebpf-agents-version NODEJS_EBPF_AGENTS_VERSION=v0.0.9"; \
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
