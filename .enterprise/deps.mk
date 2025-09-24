# Dependency Update Targets
# This file contains targets for updating Odigos OSS and enterprise dependencies

# Default OSS tag to use when updating dependencies (can be overridden)
# Use: OSS_TAG=v1.2.3 make update-odigos-oss-mod-deps-all
OSS_TAG ?= main

# Update Odigos OSS module dependencies for a specific directory
# Usage: make update-odigos-oss-mod-deps/odiglet [OSS_TAG=v1.5.0]
# This target:
# 1. Updates 'replace' directives in go.mod to use the specified OSS_TAG
# 2. Updates direct version specifications (excluding v0.0.0 versions) to use OSS_TAG
# 3. Runs 'go mod tidy' to resolve dependencies
.PHONY: update-odigos-oss-mod-deps
update-odigos-oss-mod-deps/%:
	# Using docker with alpine to ensure consistent sed behavior across platforms (GNU vs BSD sed on Linux/Mac)
	if [ -f $*/go.mod ]; then \
		docker run --rm \
			-v "$(PWD)":/workspace \
			alpine:latest \
			sh -c "cd /workspace/$* && \
				sed -i -E 's|replace (github\.com/odigos-io/odigos/[^ ]+) => (github\.com/odigos-io/odigos/[^ ]+) [^ ]+|replace \1 => \2 $(OSS_TAG)|' go.mod && \
				sed -i -E 's|^([[:space:]]*)(github\.com/odigos-io/odigos/[^ ]+) v[^0].*|\1\2 $(OSS_TAG)|' go.mod"; \
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
	@for dir in odiglet onprem-token-generator instrumentor distros central-proxy deviceplugin central-backend odigosauth; do \
		if [ -f $$dir/go.mod ]; then \
			echo "📦 Updating dependencies for $$dir..."; \
			$(MAKE) update-odigos-oss-mod-deps/$$dir OSS_TAG=$(OSS_TAG); \
		else \
			echo "⚠️  Skipping $$dir (no go.mod found)"; \
		fi; \
	done
	@echo "✅ All Odigos OSS module dependencies updated successfully!"

# Update enterprise Go instrumentation dependency in odiglet to main branch of the agent.
# Usage: make update-enterprise-go-deps
# This target specifically updates the enterprise Go instrumentation dependency
# in odiglet/go.mod to use the 'main' branch (hardcoded, no configurable tag)
# rational: we don't want to create a chore for tagging the agent repo before a release,
# since it have no runtime files, and we tend to update it a lot, it is an exception.
# this is expected to be run in the following senarios:
# 1. syncer bot that updates all dependencies to the latest on this repo main branch
# 2. forking to a new release branch (e.g. releases/v1.X.0 at which point we want the latest agent once)
# 3. run manually - for development to pull in latest agent once
.PHONY: update-enterprise-go-deps
update-golang-enterprise-deps:
	@echo "🔄 Updating enterprise Go dependencies in odiglet..."
	# Using docker with alpine to ensure consistent sed behavior across platforms (GNU vs BSD sed on Linux/Mac)
	if [ -f odiglet/go.mod ]; then \
		docker run --rm \
			-v "$(PWD)":/workspace \
			alpine:latest \
			sh -c "cd /workspace/odiglet && \
				sed -i -E 's|replace go\.opentelemetry\.io/auto => github\.com/odigos-io/enterprise-go-instrumentation v[^ ]+|replace go.opentelemetry.io/auto => github.com/odigos-io/enterprise-go-instrumentation main|' go.mod"; \
		cd odiglet && go mod tidy; \
		echo "✅ Enterprise Go dependencies updated to main"; \
	else \
		echo "❌ odiglet/go.mod not found"; \
		exit 1; \
	fi
