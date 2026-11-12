package odigosurltemplatizationexporter

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
)

const (
	defaultFlushInterval  = 5 * time.Second
	defaultFlushThreshold = 500
)

// Config defines configuration for the odigos_url_templatization exporter.
type Config struct {
	// OdigosConfigExtension references odigos_config_k8s (implements OdigosConfigExtension).
	// Used to resolve workload identity (namespace/kind/name/container) from resource attributes.
	OdigosConfigExtension *component.ID `mapstructure:"odigos_config_extension"`

	// Endpoint is the cacheDb Redis address (host:port), e.g. odigos-cache.odigos-system:6379.
	Endpoint string `mapstructure:"endpoint"`

	// MaxPathsPerWorkload is the maximum number of distinct path fields stored per
	// workload hash (server and client hashes are capped independently). When the
	// hash is full, existing paths keep incrementing and new paths are skipped.
	// Required. Autoscaler always sets this from live-traffic learning
	// (maxExamplePathsPerWorkload).
	MaxPathsPerWorkload int `mapstructure:"max_paths_per_workload"`

	// PathIdleTTL is the sliding idle window for each unmatched path field.
	// HEXPIRE is refreshed on every increment. Required. Autoscaler always sets
	// this from live-traffic learning (pathExampleIdleTTL).
	PathIdleTTL time.Duration `mapstructure:"path_idle_ttl"`

	// FlushInterval is how often pending path counts are written to Redis.
	// Counts from multiple consumeTraces batches are merged until this fires
	// or FlushThreshold is reached.
	FlushInterval time.Duration `mapstructure:"flush_interval"`

	// FlushThreshold is the number of distinct (redis key, path) pairs that
	// trigger a flush before FlushInterval. Merged counts for the same path
	// count as one.
	FlushThreshold int `mapstructure:"flush_threshold"`
}

var _ component.Config = (*Config)(nil)
var _ confmap.Validator = (*Config)(nil)

func (cfg *Config) Validate() error {
	if cfg.OdigosConfigExtension == nil {
		return errors.New("odigos_config_extension is required")
	}
	typeStr := cfg.OdigosConfigExtension.Type().String()
	if _, err := component.NewType(typeStr); err != nil {
		return fmt.Errorf("invalid odigos_config_extension type %q: %w", typeStr, err)
	}
	if cfg.Endpoint == "" {
		return errors.New("endpoint is required")
	}
	if cfg.MaxPathsPerWorkload <= 0 {
		return errors.New("max_paths_per_workload is required")
	}
	if cfg.PathIdleTTL < time.Second {
		return errors.New("path_idle_ttl is required and must be at least 1s")
	}
	if cfg.FlushInterval <= 0 {
		return errors.New("flush_interval must be positive")
	}
	if cfg.FlushThreshold <= 0 {
		return errors.New("flush_threshold must be positive")
	}
	return nil
}
