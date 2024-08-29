.PHONY: build-odiglet generate

LIBRARIES := go.opentelemetry.io/auto \
			github.com/odigos-io/ebpf-java-instrumentation \
			github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension \
			github.com/odigos-io/ebpf-generic-instrumentation \
			github.com/odigos-io/ebpf-nodejs-instrumentation \
			github.com/odigos-io/mysql-instrumentation

# For development, we can modify the libraries by adding a @<version/branch> suffix
# here, we make sure to remove the suffix to get the actual library name
CLEANED_LIBRARIES := $(patsubst %@%,%,$(foreach lib,$(LIBRARIES),$(if $(findstring @,$(lib)),$(firstword $(subst @, ,$(lib))),$(lib))))

# Convert spaces to commas to create a comma-separated list of libraries
space := $(subst ,, )
comma:= ,
GOPRIVATE := $(subst $(space),$(comma),$(CLEANED_LIBRARIES))
export GOPRIVATE

build-odiglet: generate
	go build -o odiglet ./cmd/enterprise

debug-build-odiglet: generate
	go build -gcflags "all=-N -l" -o odiglet ./cmd/enterprise

generate:
	go mod download
	@for lib in $(LIBRARIES); do \
		LIB_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
		make -C "$$LIB_PATH" generate; \
	done
