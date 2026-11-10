package odigosgroupbytraceprocessor

import (
	"errors"
	"time"
)

// Config configures odigosgroupbytraceprocessor.
type Config struct {
	// SyncWait is the longest a trace without PRODUCER/CONSUMER spans is held.
	SyncWait time.Duration `mapstructure:"sync_wait"`
	// AsyncWait is the longest a trace with a PRODUCER or CONSUMER span is held: its
	// consumer side can arrive long after the root span.
	AsyncWait time.Duration `mapstructure:"async_wait"`
	// RootSettle releases a sync trace once its root span arrived and no span came in
	// for this long. 0 disables it.
	RootSettle time.Duration `mapstructure:"root_settle"`
	// MaxTraces bounds the buffer; past it the oldest trace is released early.
	MaxTraces int `mapstructure:"max_traces"`
	// LateSpansMaxMemoryMiB bounds the memory used to remember released trace IDs for
	// late_spans; past it the oldest IDs are forgotten. 0 disables late span tracking.
	LateSpansMaxMemoryMiB int `mapstructure:"late_spans_max_memory_mib"`
}

func createDefaultConfig() *Config {
	return &Config{
		SyncWait:  30 * time.Second,
		AsyncWait: 30 * time.Second,
		// Benchmarked on replayed production-like traffic: 7s lost as few spans as
		// holding every trace 30s (~18 vs 17 late spans per 1M) while buffering ~57%
		// fewer traces; 2s lost ~10x more.
		RootSettle: 7 * time.Second,
		MaxTraces:  200_000,
		// ~540k remembered trace IDs at the ~135 bytes each one measured.
		LateSpansMaxMemoryMiB: 70,
	}
}

// Validate checks configuration.
func (c *Config) Validate() error {
	if c.SyncWait <= 0 || c.AsyncWait <= 0 {
		return errors.New("sync_wait and async_wait must be positive")
	}
	if c.RootSettle < 0 {
		return errors.New("root_settle must not be negative")
	}
	if c.MaxTraces <= 0 {
		return errors.New("max_traces must be positive")
	}
	if c.LateSpansMaxMemoryMiB < 0 {
		return errors.New("late_spans_max_memory_mib must not be negative")
	}
	return nil
}
