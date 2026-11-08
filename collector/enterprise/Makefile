
# not `git rev-parse`: the image build copies this directory without the repository
SRC_ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

TOOLS_BIN_DIR    := $(SRC_ROOT)/.tools

BUILDER_VERSION = 0.159.0
BUILDER := $(TOOLS_BIN_DIR)/builder_$(BUILDER_VERSION)
OPAMP_SUPERVISOR_DIR := $(SRC_ROOT)/cmd/odigosopampsupervisor
# mdatagen must track BUILDER_VERSION; it and goimports are pinned in internal/tools/go.mod
GO_LICENSES_VERSION = v1.6.0

CURRENT_OS := $(shell go env GOOS)
CURRENT_ARCH := $(shell go env GOHOSTARCH)
TARGET_OS ?= $(CURRENT_OS)
TARGET_ARCH ?= $(shell go env GOARCH)

BUILDER_URL = https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fbuilder%2Fv$(BUILDER_VERSION)/ocb_$(BUILDER_VERSION)_$(CURRENT_OS)_$(CURRENT_ARCH)

GOCMD?= go

.PHONY: download_builder
download_builder:
	mkdir -p $(TOOLS_BIN_DIR)
	if [ ! -f "$(BUILDER)" ]; then \
		curl -o $(BUILDER) -L $(BUILDER_URL); \
		chmod +x $(BUILDER); \
	fi

.PHONY: genodigoscol
genodigoscol: download_builder
	# GOWORK=off is needed to avoid pulling errors from other unrelated workspaces modules
	GOWORK=off $(BUILDER) --ldflags="-checklinkname=0" --config builder-config.yaml --skip-compilation

.PHONY: build-odigoscol
build-odigoscol: download_builder
	GOWORK=off $(BUILDER) \
	--ldflags="-s -w -checklinkname=0" \
	--config builder-config.yaml \
	--skip-generate \
	--skip-get-modules

# The Supervisor is a separate process and can move independently from the Collector component
# version. The v0.159.0 pin includes pre-apply validation, startup fallback, automatic
# last-known-good rollback, and the corresponding upstream process-management fixes.
.PHONY: build-opamp-supervisor
build-opamp-supervisor:
	cd $(OPAMP_SUPERVISOR_DIR) && GOWORK=off CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) \
		$(GOCMD) build -trimpath -ldflags="-s -w" -o odigosopampsupervisor .

# the enterprise-owned components; odigosotelcol is generated and internal/tools holds no buildable
# package, so both are excluded here and handled separately
OWNED_GO_MODS := $(shell find . -type f -name 'go.mod' ! -path './odigosotelcol/*' ! -path './.tools/*' ! -path './internal/tools/*' | sort)
OWNED_METADATA := $(shell find . -type f -name 'metadata.yaml' ! -path './odigosotelcol/*' ! -path './.tools/*' ! -path './internal/tools/*' | sort)

# resolved through internal/tools because mdatagen's own go.mod carries replace directives, which
# `go install` refuses to honour for a module named directly
.PHONY: install-tools
install-tools:
	mkdir -p $(TOOLS_BIN_DIR)
	cd ./internal/tools && GOWORK=off GOBIN=$(TOOLS_BIN_DIR) go install go.opentelemetry.io/collector/cmd/mdatagen
	cd ./internal/tools && GOWORK=off GOBIN=$(TOOLS_BIN_DIR) go install golang.org/x/tools/cmd/goimports

# regenerates internal/metadata from each component's metadata.yaml; unlike the targets above this
# one needs the repository, but it only ever runs in development and CI, never in the image build
.PHONY: generate
generate: install-tools
	# mdatagen resolves the import root by running `git rev-parse --show-toplevel` and reading the
	# go.mod there; this repo has no root go.mod, so point GIT_WORK_TREE at the component instead
	@REPO_ROOT="$$(git rev-parse --show-toplevel)"; \
	for META_FILE in $(OWNED_METADATA); do \
		META_DIR="$$(dirname "$$META_FILE")"; \
		MOD_ROOT="$$META_DIR"; \
		while [ "$$MOD_ROOT" != "." ] && [ "$$MOD_ROOT" != "/" ] && [ ! -f "$$MOD_ROOT/go.mod" ]; do \
			MOD_ROOT="$$(dirname "$$MOD_ROOT")"; \
		done; \
		if [ ! -f "$$MOD_ROOT/go.mod" ]; then \
			echo "no go.mod found for $$META_FILE"; \
			exit 1; \
		fi; \
		MOD_ROOT_ABS="$$(cd "$$MOD_ROOT" && pwd)"; \
		( cd "$$META_DIR" && GOWORK=off GIT_DIR="$$REPO_ROOT/.git" GIT_WORK_TREE="$$MOD_ROOT_ABS" \
			$(TOOLS_BIN_DIR)/mdatagen metadata.yaml ) || exit 1; \
	done
	$(MAKE) fmt

# mdatagen does not group imports, so the generated files are only stable once goimports has run
.PHONY: fmt
fmt: $(OWNED_GO_MODS:%=fmt/%)
fmt/%: GO_MOD=$*
fmt/%: install-tools
	cd $(shell dirname $(GO_MOD)) && $(TOOLS_BIN_DIR)/goimports -w -local github.com/odigos-io ./

# tidies each owned component module so its (indirect) odigos-io deps track the local-replaced
# siblings (common-enterprise) after an OSS dependency bump; without this, `generate`'s mdatagen
# `go list` fails with "updates to go.mod needed" once the sibling's odigos/common version changes
.PHONY: tidy
tidy: $(OWNED_GO_MODS:%=tidy/%)
tidy/%: GO_MOD=$*
tidy/%:
	cd $(shell dirname $(GO_MOD)) && GOWORK=off $(GOCMD) mod tidy

.PHONY: test
test: $(OWNED_GO_MODS:%=test/%)
	cd odigosotelcol && GOWORK=off $(GOCMD) test --ldflags="-checklinkname=0" -v ./...

test/%: GO_MOD=$*
test/%:
	cd $(shell dirname $(GO_MOD)) && GOWORK=off $(GOCMD) test --ldflags="-checklinkname=0" -v ./...

.PHONY: go-licenses
go-licenses:
	mkdir -p $(TOOLS_BIN_DIR)
	GOBIN=$(TOOLS_BIN_DIR) go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)

.PHONY: licenses
licenses: go-licenses
	# ignored: segmentio/asm is MIT-0 (undetected in subpackages), and the distribution plus the
	# components it owns are enterprise-licensed, so collector/LICENSE ships in the image directly
	# (Dockerfile copies it into /licenses; unlike OSS we do not stage it under odigosotelcol/)
	@if [ -d "$(SRC_ROOT)/odigosotelcol/licenses" ]; then \
		rm -rf $(SRC_ROOT)/odigosotelcol/licenses; \
	fi
	@if [ -d "$(OPAMP_SUPERVISOR_DIR)/licenses" ]; then \
		rm -rf $(OPAMP_SUPERVISOR_DIR)/licenses; \
	fi
	cd odigosotelcol && $(TOOLS_BIN_DIR)/go-licenses save . --save_path=licenses --ignore=github.com/segmentio/asm --ignore=odigos.io/opentelemetry-collector --ignore=github.com/odigos-io/odigos-enterprise
	cd $(OPAMP_SUPERVISOR_DIR) && $(TOOLS_BIN_DIR)/go-licenses save . --save_path=licenses --ignore=github.com/odigos-io/odigos-enterprise
	cp -R $(OPAMP_SUPERVISOR_DIR)/licenses/. $(SRC_ROOT)/odigosotelcol/licenses/
	rm -rf $(OPAMP_SUPERVISOR_DIR)/licenses

.PHONY: build
build: genodigoscol build-odigoscol build-opamp-supervisor
