.PHONY: build-odiglet debug-build-odiglet compile-odiglet generate go-mod

# OBI materialize / go-mod-replace live in obi.mk (also usable standalone for Docker caching).
include obi.mk

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

# All go.mod work: OBI replace + download.
# Expects .obi/ already materialized (from materialize-obi or a cached Docker layer).
go-mod: go-mod-replace
	@go mod download

# go-mod + each $(LIBRARIES) upstream `make generate` in parallel. Does not clone/materialize OBI.
generate: go-mod
	@pids=""; \
	for lib in $(LIBRARIES); do \
		( \
			LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
			chmod -R +w $$LIB_PATH && \
			$(MAKE) -C "$$LIB_PATH" generate \
		) & \
		pids="$$pids $$!"; \
	done; \
	fail=0; \
	for pid in $$pids; do \
		wait $$pid || fail=1; \
	done; \
	exit $$fail

# Compile only — no materialize / generate / go-mod. For Docker after those layers have run.
compile-odiglet:
	go build -ldflags="$(LD_FLAGS)" -o odiglet ./cmd/enterprise

# Local/dev build: materialize OBI first, then generate + compile (sequential so .obi exists for go-mod).
build-odiglet:
	$(MAKE) materialize-obi
	$(MAKE) generate
	$(MAKE) compile-odiglet

debug-build-odiglet:
	$(MAKE) materialize-obi
	$(MAKE) generate
	go build -gcflags "all=-N -l" -o odiglet ./cmd/enterprise
