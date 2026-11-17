.PHONY: generate tools go-licenses licenses

include obi.mk

# Libraries with a generate target in the module cache
LIBRARIES := github.com/odigos-io/runtime-detector

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

test: materialize-obi generate
	go test -a -v ./...

TOOLS := $(PWD)/.tools

.PHONY: tools
tools:
	@if [ ! -d "$(TOOLS)" ]; then \
		mkdir $(TOOLS); \
	fi

.PHONY: go-licenses
go-licenses: tools
	GOBIN=$(TOOLS) go install github.com/google/go-licenses@latest

.PHONY: licenses
licenses: go-licenses
	@if [ -d "$(PWD)/licenses" ]; then \
		rm -rf $(PWD)/licenses; \
	fi
	$(TOOLS)/go-licenses save . --save_path=licenses
