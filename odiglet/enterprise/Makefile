.PHONY: build-odiglet generate

LIBRARIES := go.opentelemetry.io/auto \
			github.com/keyval-dev/ebpf-java-instrumentation \
			github.com/keyval-dev/ebpf-java-instrumentation/otel_agent_extension \
			github.com/keyval-dev/ebpf-generic-instrumentation \
			github.com/keyval-dev/ebpf-nodejs-instrumentation \
			github.com/keyval-dev/mysql-instrumentation

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

generate:
	@for lib in $(LIBRARIES); do \
		go get $$lib; \
		GO_AUTO_PATH=$$(go list -m -f '{{.Dir}}' $${lib%@*}) && \
		cd $$GO_AUTO_PATH && \
		make generate; \
	done