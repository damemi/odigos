# Autopilot feature developer targets (included from Makefile).
#
# Response JSON Schema for LiteLLM strict mode is generated from AnalysisResponse
# Go types (jsonschema struct tags). Regenerate after changing analysis.go types.
#
# Bundled vLLM image (llm/Dockerfile) bakes model weights so cluster installs do
# not need Hugging Face at runtime.

SCHEMA_DIR := internal/autopilot/schema
SCHEMA_JSON := $(SCHEMA_DIR)/analysis_response.schema.json
SCHEMA_GO := $(SCHEMA_DIR)/response_schema_gen.go

# --- bundled vLLM / LLM image ---
VLLM_VERSION ?= v0.8.5
HF_MODEL ?= Qwen/Qwen2.5-Coder-7B-Instruct-AWQ
MODEL_DIR ?= /models/autopilot
LLM_IMAGE_NAME ?= odigos-enterprise-vllm
# Tag encodes vLLM + model so air-gapped pulls are unambiguous.
LLM_IMAGE_TAG ?= $(VLLM_VERSION)-qwen2.5-coder-7b-awq
LLM_REGISTRY ?=
PLATFORM ?= linux/amd64
LLM_DOCKERFILE := llm/Dockerfile

ifeq ($(LLM_REGISTRY),)
LLM_IMAGE := $(LLM_IMAGE_NAME):$(LLM_IMAGE_TAG)
else
LLM_IMAGE := $(LLM_REGISTRY)/$(LLM_IMAGE_NAME):$(LLM_IMAGE_TAG)
endif

.PHONY: schema-gen schema-verify llm-build llm-push llm-print-image

## schema-gen: regenerate analysis_response.schema.json and response_schema_gen.go.
schema-gen:
	cd $(SCHEMA_DIR) && GOWORK=off go generate .

## schema-verify: fail if committed schema artifacts drift from analysis.go (CI guard).
schema-verify: schema-gen
	git diff --exit-code -- $(SCHEMA_JSON) $(SCHEMA_GO)

## llm-print-image: print the fully-qualified image name that llm-build produces.
llm-print-image:
	@echo $(LLM_IMAGE)

## llm-build: build odigos-enterprise-vllm with Qwen2.5-Coder AWQ weights baked in (linux/amd64).
## Optional: VLLM_VERSION= HF_MODEL= LLM_IMAGE_TAG= LLM_REGISTRY=
llm-build:
	docker buildx build \
		--platform $(PLATFORM) \
		-f $(LLM_DOCKERFILE) \
		--build-arg VLLM_VERSION=$(VLLM_VERSION) \
		--build-arg HF_MODEL=$(HF_MODEL) \
		--build-arg MODEL_DIR=$(MODEL_DIR) \
		-t $(LLM_IMAGE) \
		--load \
		llm/

## llm-push: push the image built by llm-build (set LLM_REGISTRY first).
llm-push:
	@if [ -z "$(LLM_REGISTRY)" ]; then \
		echo "LLM_REGISTRY is required, e.g. LLM_REGISTRY=registry.depot.dev/xxx make llm-push"; \
		exit 1; \
	fi
	docker push $(LLM_IMAGE)
