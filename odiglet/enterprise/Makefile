.PHONY: debug-build-odiglet generate ensure-generated compile-odiglet go-mod

# OBI (go.opentelemetry.io/obi) materialization and go.mod replace live in ../obi.mk, so the
# Dockerfile can COPY only that file for the OBI layer and cache it independently of this
# Makefile, go.mod and sources.
include ../obi.mk

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

go-mod: go-mod-replace
	@go mod download

# Run each library's upstream `make generate` in parallel, for the libraries in $$libs.
# GOWORK=off: library dirs (module cache or local replaces) are outside the odiglet
# workspace; with GOWORK set, go commands in those dirs fail.
define generate-libs
	pids=""; \
	for lib in $$libs; do \
		( \
			LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
			chmod -R +w $$LIB_PATH && \
			GOWORK=off $(MAKE) -C "$$LIB_PATH" generate \
		) & \
		pids="$$pids $$!"; \
	done; \
	fail=0; \
	for pid in $$pids; do \
		wait $$pid || fail=1; \
	done; \
	exit $$fail
endef

# go-mod + each $(LIBRARIES) upstream `make generate`. Does not materialize OBI.
generate: go-mod
	@libs="$(LIBRARIES)"; \
	$(generate-libs)

# The generated eBPF objects live in the module cache, a BuildKit cache mount in the Dockerfile.
# When the generate layer is reused but the mount came back empty (evicted, or a new cache-mount
# namespace), compile would fail on missing *_bpfel.o. Regenerate only what is missing.
ensure-generated: go-mod
	@libs=""; \
	for lib in $(LIBRARIES); do \
		LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) || exit 1; \
		if [ -z "$$(find "$$LIB_PATH" -name '*_bpfel.o' -print -quit)" ]; then \
			libs="$$libs $$lib"; \
		fi; \
	done; \
	if [ -z "$$libs" ]; then exit 0; fi; \
	echo "==> Generated eBPF objects missing from the module cache, regenerating:$$libs"; \
	$(generate-libs)

# Compile only - no materialize / generate / go-mod. For Docker after those layers have run.
compile-odiglet:
	CGO_ENABLED=0 go build -ldflags="$(LD_FLAGS)" -o odiglet ./cmd/enterprise

debug-build-odiglet: materialize-obi generate
	go build -gcflags "all=-N -l" -o odiglet ./cmd/enterprise
