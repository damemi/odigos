gqlgen:
	go get github.com/99designs/gqlgen/internal/imports@latest && go get github.com/99designs/gqlgen@latest && go run github.com/99designs/gqlgen generate


## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: go-licenses
go-licenses: $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/google/go-licenses@latest

# collector/odigosotelcol has to be run separately due to current ambiguous import upstream
.PHONY: licenses
licenses: go-licenses
	rm -rf $(PWD)/licenses
	$(LOCALBIN)/go-licenses save . --save_path=licenses --ignore github.com/odigos-io/central-backend
