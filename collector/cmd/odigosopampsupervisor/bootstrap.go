// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/config"
	"go.uber.org/zap"
)

// EnvBootstrapConfig is a base64-encoded Collector config for cold starts when OpAMP is unreachable.
// Ephemeral storage has no last-known config, so without this the Supervisor falls back to the inert
// bundled config. Must arrive as a secret-store reference (contains exporter credentials), not a plain env value.
const EnvBootstrapConfig = "ODIGOS_COLLECTOR_BOOTSTRAP_CONFIG"

// BootstrapConfigFileName is the decoded document under storage.directory.
const BootstrapConfigFileName = "bootstrap-config.yaml"

const bootstrapFileMode = 0o600
const bootstrapStorageMode = 0o700

// installBootstrapConfig installs a supplied bootstrap config as the Supervisor's startup fallback.
//
// The bootstrap document replaces the bundled inert config (not merged after it): upstream merges
// startup_fallback_configs in order and confmap replaces lists, so an inert pipelines list would wipe
// the real one. Local files from agent::config_files (e.g. guardrails.yaml) follow so they load on
// cold start too. Failures are non-fatal — leave the configured fallback unchanged.
func installBootstrapConfig(cfg *config.Supervisor, logger *zap.Logger) {
	encoded := strings.TrimSpace(os.Getenv(EnvBootstrapConfig))
	path := bootstrapConfigPath(cfg.Storage.Directory)

	if encoded == "" {
		// Clear any leftover file from a prior deployment (may hold exporter credentials).
		removeStaleBootstrapConfig(path, logger)
		return
	}

	document, err := decodeBootstrapConfig(encoded)
	if err != nil {
		logger.Error("ignoring the bootstrap configuration supplied by the deployment; a cold start "+
			"without the control plane will serve the inert bundled configuration instead",
			zap.String("variable", EnvBootstrapConfig), zap.Error(err))
		removeStaleBootstrapConfig(path, logger)
		return
	}

	if err := writeBootstrapConfig(path, document); err != nil {
		logger.Error("could not write the bootstrap configuration; a cold start without the control "+
			"plane will serve the inert bundled configuration instead",
			zap.String("path", path), zap.Error(err))
		return
	}

	candidate := cfg.Agent
	candidate.StartupFallbackConfigs = append([]string{path}, localConfigFiles(cfg.Agent.ConfigFiles)...)
	if err := candidate.Validate(); err != nil {
		logger.Error("the bootstrap configuration supplied by the deployment is not one this Collector "+
			"can serve, so it is ignored; a cold start without the control plane will serve the inert "+
			"bundled configuration instead",
			zap.String("variable", EnvBootstrapConfig), zap.Error(err))
		removeStaleBootstrapConfig(path, logger)
		return
	}

	cfg.Agent = candidate
	logger.Info("a cold start without the control plane will serve the bootstrap configuration supplied "+
		"by the deployment",
		zap.Strings("startup_fallback_configs", cfg.Agent.StartupFallbackConfigs))
}

func bootstrapConfigPath(storageDirectory string) string {
	return filepath.Join(storageDirectory, BootstrapConfigFileName)
}

// decodeBootstrapConfig decodes the env value. Whitespace is stripped first so IaC wrapping is tolerated.
func decodeBootstrapConfig(encoded string) ([]byte, error) {
	var packed strings.Builder
	packed.Grow(len(encoded))
	for _, r := range encoded {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		}
		packed.WriteRune(r)
	}

	document, err := base64.StdEncoding.DecodeString(packed.String())
	if err != nil {
		return nil, fmt.Errorf("%s is not base64: %w", EnvBootstrapConfig, err)
	}
	if len(strings.TrimSpace(string(document))) == 0 {
		// Empty configs validate as healthy Collectors with no pipelines — refuse rather than write.
		return nil, errors.New("the decoded bootstrap configuration is empty")
	}
	return document, nil
}

// writeBootstrapConfig writes the document, creating storage.directory on first boot if needed.
func writeBootstrapConfig(path string, document []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, bootstrapStorageMode); err != nil {
			return fmt.Errorf("create the Supervisor storage directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, document, bootstrapFileMode); err != nil {
		return fmt.Errorf("write the bootstrap configuration: %w", err)
	}
	return nil
}

func removeStaleBootstrapConfig(path string, logger *zap.Logger) {
	err := os.Remove(path)
	switch {
	case err == nil:
		logger.Info("removed a bootstrap configuration left by an earlier deployment", zap.String("path", path))
	case errors.Is(err, os.ErrNotExist):
	default:
		// Not fatal — file is unused — but it may still hold exporter credentials.
		logger.Warn("could not remove a bootstrap configuration left by an earlier deployment",
			zap.String("path", path), zap.Error(err))
	}
}

// localConfigFiles returns real paths from agent::config_files, dropping $REMOTE_CONFIG and other
// Supervisor placeholders that are meaningless in a fallback list.
func localConfigFiles(configFiles []string) []string {
	out := make([]string, 0, len(configFiles))
	for _, file := range configFiles {
		if strings.HasPrefix(file, "$") {
			continue
		}
		out = append(out, file)
	}
	return out
}
