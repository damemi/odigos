# Dependency Update Targets
# This file contains targets for updating Odigos OSS and enterprise dependencies

# Default OSS tag to use when updating dependencies (can be overridden)
# Use: OSS_TAG=v1.2.3 make update-odigos-oss-mod-deps-all
OSS_TAG ?= main

# Update Odigos OSS module dependencies for a specific directory
# Usage: make update-odigos-oss-mod-deps/odiglet [OSS_TAG=v1.5.0]
# This target:
# 1. Updates 'replace' directives in go.mod to use the specified OSS_TAG
# 2. Updates direct version specifications to use OSS_TAG, except the unpublished
#    local-replace placeholder v0.0.0-00010101000000-000000000000 (other v0.0.0 /
#    v0.0.0-feat.* pins are rewritten).
# 3. Runs 'go mod tidy' to resolve dependencies
.PHONY: update-odigos-oss-mod-deps
update-odigos-oss-mod-deps/%:
	# Using docker with alpine to ensure consistent sed behavior across platforms (GNU vs BSD sed on Linux/Mac)
	if [ -f $*/go.mod ]; then \
		CONTAINER_ID=$$(hostname); \
		HOST_PATH=$$(docker inspect $$CONTAINER_ID --format='{{range .Mounts}}{{if eq .Destination "/workspaces/odigos-enterprise"}}{{.Source}}{{end}}{{end}}' 2>/dev/null); \
		if [ -z "$$HOST_PATH" ]; then HOST_PATH="$(PWD)"; fi; \
		docker run --rm \
			-v "$$HOST_PATH":/workspace \
			alpine:latest \
			sh -c "cd /workspace/$* && \
				sed -i -E 's|replace (github\.com/odigos-io/odigos/[^ ]+) => (github\.com/odigos-io/odigos/[^ ]+) [^ ]+|replace \1 => \2 $(OSS_TAG)|' go.mod && \
				sed -i -E '/v0\.0\.0-00010101000000-000000000000/!s|^([[:space:]]*)(github\.com/odigos-io/odigos/[^ ]+) v[^ ]+|\1\2 $(OSS_TAG)|' go.mod"; \
		echo "✅ running go mod tidy for $*"; \
		cd $* && go mod tidy; \
	fi

# Update Odigos OSS module dependencies for all relevant directories
# Usage: make update-odigos-oss-mod-deps-all [OSS_TAG=v1.5.0]
# This target iterates through all directories with go.mod files and updates their
# Odigos OSS dependencies to the specified OSS_TAG (defaults to 'main')
.PHONY: update-odigos-oss-mod-deps-all
update-odigos-oss-mod-deps-all:
	@echo "🔄 Updating Odigos OSS module dependencies for all relevant directories..."
	@echo "📌 Using OSS_TAG: $(OSS_TAG)"
	@for dir in odiglet onprem-token-generator instrumentor distros central-proxy central-backend odigosauth ebpfshare ui; do \
		if [ -f $$dir/go.mod ]; then \
			echo "📦 Updating dependencies for $$dir..."; \
			$(MAKE) update-odigos-oss-mod-deps/$$dir OSS_TAG=$(OSS_TAG); \
		else \
			echo "⚠️  Skipping $$dir (no go.mod found)"; \
		fi; \
	done
	$(MAKE) update-collector-oss-deps OSS_TAG=$(OSS_TAG)
	@echo "✅ All Odigos OSS module dependencies updated successfully!"

# Re-pin the Odigos collector components (Odigos' own, not upstream OpenTelemetry) that
# collector/builder-config.yaml consumes. Upstream versions arrive transitively through them.
# Usage: make update-collector-oss-deps [OSS_TAG=v1.5.0]
# This target:
# 1. Asks the module proxy for each component at OSS_TAG - odigos-io/odigos tags every
#    collector module on release, so the tag resolves per module
# 2. Rewrites those versions in the manifest, where OSS instead points at sibling directories
# 3. Regenerates the distribution, writing nothing unless every module and path check passes
# Using docker with alpine to ensure consistent sed behavior across platforms (GNU vs BSD sed on Linux/Mac)
DOCKER_SED := docker run --rm -i alpine:latest sed

# One substitution per component overflows BSD sed's command buffer, so pass it as a script file
DOCKER_SED_SCRIPT := docker run --rm -i -e SED_SCRIPT alpine:latest \
	sh -c 'printf "%s\n" "$$SED_SCRIPT" > /tmp/rewrite.sed && exec sed -E -f /tmp/rewrite.sed'

.PHONY: update-collector-oss-deps
update-collector-oss-deps:
	@echo "📦 Re-pinning Odigos OSS collector components to $(OSS_TAG)..."
	@if ! command -v docker >/dev/null 2>&1; then \
		echo "❌ docker is required: the manifest rewrites run in a container so that mac and CI behave identically"; \
		exit 1; \
	fi
	@set -e; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	(cd "$$tmp" && go mod init odigos.io/collector-dep-resolver >/dev/null); \
	$(DOCKER_SED) -nE 's|^[[:space:]]*- (.*) => (github\.com/odigos-io/odigos[^ ]*) v.*|\1 \2|p' < collector/builder-config.yaml | sort -u > "$$tmp/pairs"; \
	if [ ! -s "$$tmp/pairs" ]; then \
		echo "❌ no odigos-io/odigos replace targets found in collector/builder-config.yaml"; exit 1; \
	fi; \
	bad=0; \
	while read -r lhs mod; do \
		out=$$(cd "$$tmp" && GOWORK=off GOFLAGS=-mod=mod go list -m -f '{{.Version}}|{{.GoMod}}' "$$mod@$(OSS_TAG)" 2>"$$tmp/err") || out=""; \
		ver=$${out%%|*}; gomod=$${out#*|}; \
		if [ -z "$$ver" ] || [ ! -f "$$gomod" ]; then \
			echo "   ❌ $$mod: $$(tail -n1 "$$tmp/err")"; bad=1; continue; \
		fi; \
		declared=$$(awk '/^module /{print $$2; exit}' "$$gomod"); \
		if [ "$$declared" != "$$lhs" ]; then \
			echo "   ❌ $$mod declares its path as $$declared, but is required as $$lhs"; bad=1; continue; \
		fi; \
		echo "$$mod $$ver" >> "$$tmp/pins"; \
	done < "$$tmp/pairs"; \
	if [ $$bad -ne 0 ]; then \
		echo ""; \
		echo "❌ manifest is inconsistent with odigos-io/odigos at $(OSS_TAG) - left unchanged"; \
		echo "   a moved component needs its replace target updated; a path mismatch needs the"; \
		echo "   left-hand side to match the component's module line, which is what Go validates"; \
		exit 1; \
	fi; \
	: > "$$tmp/rewrite.sed"; \
	while read -r mod ver; do \
		printf '%s\n' "s|(=> $$mod) v[^ ]+|\1 $$ver|g" >> "$$tmp/rewrite.sed"; \
	done < "$$tmp/pins"; \
	SED_SCRIPT="$$(cat "$$tmp/rewrite.sed")" \
		$(DOCKER_SED_SCRIPT) < collector/builder-config.yaml > "$$tmp/manifest"; \
	if [ ! -s "$$tmp/manifest" ]; then \
		echo "❌ the rewrite produced an empty manifest - collector/builder-config.yaml was left unchanged"; exit 1; \
	fi; \
	cat "$$tmp/manifest" > collector/builder-config.yaml; \
	echo "✅ $$(wc -l < "$$tmp/pins" | tr -d ' ') components pinned at $(OSS_TAG)"
	@$(MAKE) -C collector genodigoscol
	@echo "✅ collector/odigosotelcol regenerated - commit it with the manifest"

# Update enterprise Go instrumentation dependency in odiglet to main branch of the agent.
# Usage: make update-golang-enterprise-deps
# This target specifically updates the enterprise Go instrumentation dependency
# in odiglet/go.mod to use the 'main' branch (hardcoded, no configurable tag)
# rational: we don't want to create a chore for tagging the agent repo before a release,
# since it have no runtime files, and we tend to update it a lot, it is an exception.
# this is expected to be run in the following senarios:
# 1. syncer bot that updates all dependencies to the latest on this repo main branch
# 2. forking to a new release branch (e.g. releases/v1.X.0 at which point we want the latest agent once)
# 3. run manually - for development to pull in latest agent once

GOLANG_ENTERPRISE_TAG := $(if $(GOLANG_ENTERPRISE_TAG),$(GOLANG_ENTERPRISE_TAG),main)
.PHONY: update-golang-enterprise-deps
update-golang-enterprise-deps:
	@echo "🔄 Updating enterprise Go dependencies in odiglet..."
	# Using docker with alpine to ensure consistent sed behavior across platforms (GNU vs BSD sed on Linux/Mac)
	if [ -f odiglet/go.mod ]; then \
		CONTAINER_ID=$$(hostname); \
		HOST_PATH=$$(docker inspect $$CONTAINER_ID --format='{{range .Mounts}}{{if eq .Destination "/workspaces/odigos-enterprise"}}{{.Source}}{{end}}{{end}}' 2>/dev/null); \
		if [ -z "$$HOST_PATH" ]; then HOST_PATH="$(PWD)"; fi; \
		docker run --rm \
			-v "$$HOST_PATH":/workspace \
			alpine:latest \
			sh -c "cd /workspace/odiglet && \
				sed -i -E 's|replace go\.opentelemetry\.io/auto => github\.com/odigos-io/enterprise-go-instrumentation .*|replace go.opentelemetry.io/auto => github.com/odigos-io/enterprise-go-instrumentation $(GOLANG_ENTERPRISE_TAG)|' go.mod"; \
		cd odiglet && go mod tidy; \
		echo "✅ Enterprise Go dependencies updated to $(GOLANG_ENTERPRISE_TAG)"; \
	else \
		echo "❌ odiglet/go.mod not found"; \
		exit 1; \
	fi
