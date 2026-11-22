package odigosautopilotprofilesexporter

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
)

const (
	// Matches OdigosClusterCollectorTraceAggregationWaitDurationDefault.
	defaultGroupByTraceWaitDuration = 30 * time.Second
	defaultFlushInterval            = 5 * time.Second
	defaultFlushThreshold           = 500
)

// Config configures the autopilot profiles exporter.
// Linked sample stacks are written to cacheDb Redis for the insights
// autopilot service to correlate into call-tree structure.
type Config struct {
	// RedisEndpoint is the cacheDb Redis address (host:port),
	// e.g. odigos-cache.odigos-system:6379.
	RedisEndpoint string `mapstructure:"redis_endpoint"`

	// GroupByTraceWaitDuration is the gateway groupbytrace wait_duration.
	// Redis whole-trace keys expire at twice this window so stacks remain
	// available through aggregation plus a matching join margin.
	GroupByTraceWaitDuration time.Duration `mapstructure:"group_by_trace_wait_duration"`

	// FlushInterval is how often pending profile stacks are written to Redis.
	// Stacks from multiple ConsumeProfiles batches are merged until this fires
	// or FlushThreshold is reached.
	FlushInterval time.Duration `mapstructure:"flush_interval"`

	// FlushThreshold is the number of pending stacks that trigger a flush
	// before FlushInterval.
	FlushThreshold int `mapstructure:"flush_threshold"`
}

func (c *Config) Validate() error {
	if c.RedisEndpoint == "" {
		return fmt.Errorf("redis_endpoint is required")
	}
	if c.GroupByTraceWaitDuration <= 0 {
		return fmt.Errorf("group_by_trace_wait_duration must be positive")
	}
	if c.FlushInterval <= 0 {
		return fmt.Errorf("flush_interval must be positive")
	}
	if c.FlushThreshold <= 0 {
		return fmt.Errorf("flush_threshold must be positive")
	}
	return nil
}

var _ component.Config = (*Config)(nil)
