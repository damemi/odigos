# Odigos autopilot vLLM image with model weights baked in.
# Runtime does not contact Hugging Face — weights live under MODEL_DIR.
#
# Build (from insights/):
#   make llm-build
#   make llm-push LLM_REGISTRY=registry.example.com
# Published on demand by .github/workflows/release-vllm.yml, not by the Odigos release.
#
# Default: Qwen2.5-Coder-7B-Instruct AWQ (~5–8Gi weights). Prefer linux/amd64 for GKE GPU nodes.

ARG VLLM_VERSION=v0.8.5
FROM vllm/vllm-openai:${VLLM_VERSION}

ARG HF_MODEL=Qwen/Qwen2.5-Coder-7B-Instruct-AWQ
ARG MODEL_DIR=/models/autopilot

USER root
RUN mkdir -p "${MODEL_DIR}" \
 && python3 -c "\
from huggingface_hub import snapshot_download; \
snapshot_download(repo_id='${HF_MODEL}', local_dir='${MODEL_DIR}')" \
 && chmod -R a+rX "${MODEL_DIR}"

ENV ODIGOS_VLLM_MODEL_PATH=${MODEL_DIR}
ENV ODIGOS_VLLM_SERVED_MODEL_NAME=autopilot
