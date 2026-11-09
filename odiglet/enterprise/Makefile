.PHONY: build-odiglet generate setup-obi setup-obi-pin setup-obi-release build-modular-docker build-final-docker

# ---------------- OBI (go.opentelemetry.io/obi) ----------------
# The go module cache tarball for OBI does NOT include the bpf2go-generated *_bpfel.go / *.o files,
# so we materialize them locally and add a local-path go.mod replace before `go build`. Two methods
# are supported; `setup-obi` dispatches to one via OBI_SETUP (pin|release):
#   - setup-obi-pin:     clone a pinned commit (OBI_COMMIT) and run OBI's bpf2go codegen in the clone.
#   - setup-obi-release: download + verify OBI's published source-generated release tarball (OBI_VERSION).
# Keep the go.opentelemetry.io/obi version in go.mod (require) in sync with the selected method:
# the OBI_COMMIT pseudo-version for pin, or the OBI_VERSION tag for release.
# Pinned to the same OBI commit as OSS odigos (see odiglet/Makefile in odigos-io/odigos).
OBI_MODULE := go.opentelemetry.io/obi
OBI_SETUP ?= pin
OBI_LOCAL_DIR := $(abspath $(CURDIR)/.obi)

# --- pin method (clone commit + bpf2go codegen) ---
OBI_COMMIT ?= c76a93c8775c4c66dae4af24c66b9a0f409a2e49
OBI_GIT_URL := https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation
OBI_CLONE_DIR := $(OBI_LOCAL_DIR)/opentelemetry-ebpf-instrumentation-$(OBI_COMMIT)
# Relative path for the transient go.mod replace (portable in docker build / bind mounts).
OBI_PIN_REPLACE_PATH := ./.obi/opentelemetry-ebpf-instrumentation-$(OBI_COMMIT)

# --- release method (source-generated tarball) ---
OBI_VERSION ?= v0.10.0
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

# Dispatch to the selected OBI setup method (OBI_SETUP=pin|release).
# Phony: prerequisite of `generate` (so `build-odiglet` / `make generate` refresh OBI first).
setup-obi: setup-obi-$(OBI_SETUP)

# Pin method: clone the pinned OBI commit and run bpf2go codegen in the clone (generate/all in-tree
# when docker is unavailable - e.g. inside a docker build; docker-generate on the host), then add a
# transient local-path go.mod replace so go build can resolve the generated bpf objects.
setup-obi-pin:
	@mkdir -p $(OBI_LOCAL_DIR)
	@if [ ! -d "$(OBI_CLONE_DIR)/.git" ]; then \
		git init -q "$(OBI_CLONE_DIR)"; \
		git -C "$(OBI_CLONE_DIR)" remote add origin "$(OBI_GIT_URL)"; \
	fi
	@git -C "$(OBI_CLONE_DIR)" fetch --depth 1 origin "$(OBI_COMMIT)"
	@git -C "$(OBI_CLONE_DIR)" checkout -q --detach FETCH_HEAD
	@if [ -f /.dockerenv ] || ! command -v docker >/dev/null 2>&1; then \
		echo "==> Generating OBI bpf2go artifacts in-tree (no docker)"; \
		$(MAKE) -C "$(OBI_CLONE_DIR)" generate/all; \
	else \
		echo "==> Generating OBI bpf2go artifacts via docker-generate"; \
		$(MAKE) -C "$(OBI_CLONE_DIR)" docker-generate; \
	fi
	@go mod edit -replace "$(OBI_MODULE)=$(OBI_PIN_REPLACE_PATH)"
	@go mod tidy

# Release method: download and verify OBI's source-generated release tarball, extract under .obi/,
# then add a local-path go.mod replace.
setup-obi-release:
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
	CGO_ENABLED=0 go build -ldflags="$(LD_FLAGS)" -o odiglet ./cmd/enterprise

debug-build-odiglet: generate
	go build -gcflags "all=-N -l" -o odiglet ./cmd/enterprise

# OBI: `setup-obi` (dispatches to setup-obi-pin or setup-obi-release via OBI_SETUP).
# Then `go mod download` and each $(LIBRARIES) upstream `make generate`.
generate: setup-obi
	go mod download
	@for lib in $(LIBRARIES); do \
		LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
		chmod -R +w $$LIB_PATH && \
		make -C "$$LIB_PATH" generate; \
	done
