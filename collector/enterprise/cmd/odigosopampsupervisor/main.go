// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// odigosopampsupervisor is the managed-mode entrypoint shipped alongside the Odigos enterprise
// Collector. It intentionally remains a thin wrapper around the upstream Supervisor so security,
// validation, and rollback fixes can be upgraded without forking its process-management logic.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor"
	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/config"
	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/telemetry"
	"go.opentelemetry.io/collector/featuregate"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	configPath := flag.String("config", "", "Path to a Supervisor configuration file")
	featuregate.GlobalRegistry().RegisterFlags(flag.CommandLine)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	logger, err := telemetry.NewLogger(cfg.Telemetry.Logs)
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	// After Load and before NewSupervisor, which is the only window it fits in: it needs the resolved
	// storage.directory and agent::config_files to know where to write and what to merge, and the
	// Supervisor reads startup_fallback_configs when it starts. Never fatal — see installBootstrapConfig.
	installBootstrapConfig(&cfg, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := supervisor.NewSupervisor(ctx, logger.Named("supervisor"), cfg)
	if err != nil {
		return fmt.Errorf("failed to create supervisor: %w", err)
	}
	if err := s.Start(ctx); err != nil {
		return fmt.Errorf("failed to start supervisor: %w", err)
	}

	interrupt := make(chan os.Signal, 2)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	<-interrupt
	s.Shutdown()

	return nil
}
