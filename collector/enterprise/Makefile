
# not `git rev-parse`: the image build copies this directory without the repository
SRC_ROOT := $(patsubst %/,%,$(dir $(abspath $(lastword $(MAKEFILE_LIST)))))

TOOLS_BIN_DIR    := $(SRC_ROOT)/.tools

BUILDER_VERSION = 0.151.0
BUILDER := $(TOOLS_BIN_DIR)/builder_$(BUILDER_VERSION)

CURRENT_OS := $(shell go env GOOS)
CURRENT_ARCH := $(shell go env GOHOSTARCH)

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

.PHONY: test
test:
	cd odigosotelcol && GOWORK=off $(GOCMD) test --ldflags="-checklinkname=0" -v ./...

.PHONY: go-licenses
go-licenses:
	mkdir -p $(TOOLS_BIN_DIR)
	GOBIN=$(TOOLS_BIN_DIR) go install github.com/google/go-licenses@latest

.PHONY: licenses
licenses: go-licenses
	# ignored: segmentio/asm is MIT-0 (undetected in subpackages), and the distribution itself
	# is enterprise-licensed, so collector/LICENSE ships in the image directly
	@if [ -d "$(SRC_ROOT)/odigosotelcol/licenses" ]; then \
		rm -rf $(SRC_ROOT)/odigosotelcol/licenses; \
	fi
	cd odigosotelcol && $(TOOLS_BIN_DIR)/go-licenses save . --save_path=licenses --ignore=github.com/segmentio/asm --ignore=odigos.io/opentelemetry-collector

.PHONY: build
build: genodigoscol build-odigoscol
