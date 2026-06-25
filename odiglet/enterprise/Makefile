.PHONY: build-odiglet generate setup-obi build-modular-docker build-final-docker

# go.opentelemetry.io/obi (module path in go mod edit -replace; must match go.mod)
OBI_MODULE := go.opentelemetry.io/obi
# Upstream source-generated release (https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation).
# Keep in sync with go.mod (go list -m go.opentelemetry.io/obi) when upgrading OBI.
OBI_VERSION ?= v0.8.0
OBI_LOCAL_DIR := $(abspath $(CURDIR)/.obi)
OBI_EXTRACTED := $(OBI_LOCAL_DIR)/obi-$(OBI_VERSION)-source-generated
OBI_ARCHIVE := obi-$(OBI_VERSION)-source-generated.tar.gz
OBI_RELEASE_BASE := https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/releases/download

# Libraries that need generate run in module cache (must have a generate target that works without git).
LIBRARIES := go.opentelemetry.io/auto \
			github.com/odigos-io/ebpf-java-instrumentation/v2 \
			github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/v2 \
			github.com/odigos-io/ebpf-core \
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

# OBI: download/verify the upstream source-generated tarball, extract under .obi/, go mod replace.
# Phony: prerequisite of `generate` (so `build-odiglet` / `make generate` use the artifact, not go generate in-tree).
setup-obi:
	@mkdir -p $(OBI_LOCAL_DIR)/dl
	@set -e; \
		download_dir="$(OBI_LOCAL_DIR)/dl"; \
		versioned_release_url="$(OBI_RELEASE_BASE)/$(OBI_VERSION)"; \
		tarball_name="$(OBI_ARCHIVE)"; \
		cd "$$download_dir"; \
		curl -fsSL "$$versioned_release_url/SHA256SUMS" -o SHA256SUMS; \
		curl -fsSL "$$versioned_release_url/$$tarball_name" -o "$$tarball_name"; \
		expected_sha256=$$(grep -F "$$tarball_name" SHA256SUMS | awk '{print $$1}'); \
		computed_sha256=$$(openssl dgst -sha256 "$$tarball_name" | awk '{print $$NF}'); \
		test "$$expected_sha256" = "$$computed_sha256" || (echo "OBI archive checksum mismatch" && exit 1); \
		rm -rf "$(OBI_EXTRACTED)"; \
		tar -xzf "$$tarball_name" -C $(OBI_LOCAL_DIR)
	@go mod edit -replace "$(OBI_MODULE)=$(OBI_EXTRACTED)"
	@go mod tidy

build-odiglet: generate
	go build -ldflags="$(LD_FLAGS)" -o odiglet ./cmd/enterprise

debug-build-odiglet: generate
	o build -gcflags "all=-N -l" -o odiglet ./cmd/enterprise

# OBI: `setup-obi` (source-generated tarball + go mod replace), not `go generate` on the raw module.
# Then `go mod download` and each $(LIBRARIES) upstream `make generate`.
generate: setup-obi
	go mod download
	@for lib in $(LIBRARIES); do \
		LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
		chmod -R +w $$LIB_PATH && \
		make -C "$$LIB_PATH" generate; \
	done
