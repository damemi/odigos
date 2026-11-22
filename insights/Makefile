# Insights service developer targets.
#
# API contract tooling: openapi.yaml (api/openapi.yaml) is the single source of
# truth. `api-gen` regenerates the Go models and the TypeScript client from it;
# `api-lint` validates the spec; `api-mock` runs a mock server for the frontend.

include autopilot.mk

OPENAPI := api/openapi.yaml
OAPI_CONFIG := api/oapi-codegen.yaml
GO_MODELS := internal/api/http/apitypes/models.gen.go
TS_CLIENT := api/client/api.d.ts
# Pinned so api-verify doesn't fail when a new release changes the generated header.
OAPI_CODEGEN_VERSION ?= v2.8.0

.PHONY: api-gen api-gen-go api-gen-ts api-lint api-mock api-verify port-forward port-forward-stop \
	loadtest-start loadtest-stop loadtest-restart

## api-gen: regenerate both the Go models and the TS client from the spec.
api-gen: api-gen-go api-gen-ts

## api-gen-go: generate the Go wire-type models (package apitypes) from the spec.
api-gen-go:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) \
		--config $(OAPI_CONFIG) $(OPENAPI)

## api-gen-ts: generate the frontend TypeScript types from the spec.
api-gen-ts:
	npx --yes openapi-typescript@latest $(OPENAPI) -o $(TS_CLIENT)

## api-verify: fail if the committed Go models drift from the spec (CI guard).
api-verify: api-gen-go
	git diff --exit-code -- $(GO_MODELS)

## api-lint: lint the OpenAPI spec (requires redocly or spectral).
api-lint:
	npx --yes @redocly/cli@latest lint $(OPENAPI)

## api-mock: serve a mock API from the spec on :4010 for frontend development.
api-mock:
	npx --yes @stoplight/prism-cli@latest mock $(OPENAPI)

# Background port-forward to the insights service API at http://localhost:<port>.
INSIGHTS_NS ?= odigos-system
INSIGHTS_PORT ?= 8080
INSIGHTS_PF_PID := /tmp/odigos-insights-port-forward.pid
INSIGHTS_PF_LOG := /tmp/odigos-insights-port-forward.log

## port-forward: background kubectl port-forward to deploy/odigos-insights.
port-forward:
	@if [ -f $(INSIGHTS_PF_PID) ] && kill -0 $$(cat $(INSIGHTS_PF_PID)) 2>/dev/null; then \
		echo "⚠️  insights port-forward already running (pid $$(cat $(INSIGHTS_PF_PID))). Run 'make port-forward-stop' first."; \
		exit 0; \
	fi
	@echo "▶️  port-forwarding deploy/odigos-insights ($(INSIGHTS_NS)) to localhost:$(INSIGHTS_PORT) in the background..."
	@kubectl port-forward -n $(INSIGHTS_NS) deploy/odigos-insights $(INSIGHTS_PORT):8080 > $(INSIGHTS_PF_LOG) 2>&1 & echo $$! > $(INSIGHTS_PF_PID)
	@sleep 1
	@if kill -0 $$(cat $(INSIGHTS_PF_PID)) 2>/dev/null; then \
		echo "✅ ready: http://localhost:$(INSIGHTS_PORT)/api/v1  (pid $$(cat $(INSIGHTS_PF_PID)), logs: $(INSIGHTS_PF_LOG))"; \
		echo "   stop with: make port-forward-stop"; \
	else \
		echo "❌ port-forward failed to start:"; cat $(INSIGHTS_PF_LOG); rm -f $(INSIGHTS_PF_PID); exit 1; \
	fi

## port-forward-stop: stop the background insights port-forward.
port-forward-stop:
	@if [ -f $(INSIGHTS_PF_PID) ]; then \
		kill $$(cat $(INSIGHTS_PF_PID)) 2>/dev/null || true; \
		rm -f $(INSIGHTS_PF_PID); \
		echo "🛑 stopped insights port-forward"; \
	else \
		echo "no insights port-forward running (no pid file)"; \
	fi

# In-cluster OTLP loadtest (see loadtest/Makefile). Requires insights Postgres Ready.
## loadtest-start: deploy k6 loadtest (optional VUS=20 SLEEP_MS=10 TENANTS=10)
loadtest-start:
	$(MAKE) -C loadtest start VUS="$(VUS)" SLEEP_MS="$(SLEEP_MS)" DURATION="$(DURATION)" ENDPOINT="$(ENDPOINT)" TENANTS="$(TENANTS)"

## loadtest-stop: remove k6 loadtest Deployment
loadtest-stop:
	$(MAKE) -C loadtest stop