package odigosautopilotprofilesexporter

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
	"go.uber.org/zap"
)

const profileFrameTypeKey = "profile.frame.type"

type autopilotProfilesExporter struct {
	logger  *zap.Logger
	cfg     *Config
	batcher *stackBatcher
}

func newExporter(logger *zap.Logger, cfg *Config) *autopilotProfilesExporter {
	return &autopilotProfilesExporter{
		logger:  logger,
		cfg:     cfg,
		batcher: newStackBatcher(cfg.FlushInterval, cfg.FlushThreshold, logger),
	}
}

func (e *autopilotProfilesExporter) Start(context.Context, component.Host) error {
	keyTTL := 2 * e.cfg.GroupByTraceWaitDuration
	rdb, err := openProfilesRedis(e.cfg.RedisEndpoint, keyTTL, e.logger)
	if err != nil {
		e.logger.Error("autopilot profiles redis unavailable; continuing without it",
			zap.String("endpoint", e.cfg.RedisEndpoint),
			zap.Error(err),
		)
		return nil
	}
	e.batcher.start(rdb)
	e.logger.Info("autopilot profiles redis connected",
		zap.String("endpoint", e.cfg.RedisEndpoint),
		zap.Duration("group_by_trace_wait_duration", e.cfg.GroupByTraceWaitDuration),
		zap.Duration("redis_key_ttl", keyTTL),
		zap.Duration("flush_interval", e.batcher.flushInterval),
		zap.Int("flush_threshold", e.batcher.flushThreshold),
	)
	return nil
}

func (e *autopilotProfilesExporter) Shutdown(context.Context) error {
	if err := e.batcher.stop(); err != nil {
		e.logger.Warn("autopilot profiles redis close failed", zap.Error(err))
		return err
	}
	return nil
}

func (e *autopilotProfilesExporter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeProfiles queues linked sample stacks to Redis for later correlation
// by the insights autopilot service.
func (e *autopilotProfilesExporter) ConsumeProfiles(_ context.Context, profiles pprofile.Profiles) error {
	if !e.batcher.enabled() {
		return nil
	}
	for _, s := range profileStacksFrom(profiles) {
		e.batcher.recordStack(s)
	}
	return nil
}

func locationFrameType(
	loc pprofile.Location,
	attrTable pprofile.KeyValueAndUnitSlice,
	stringTable pcommon.StringSlice,
) string {
	idxs := loc.AttributeIndices()
	for i := 0; i < idxs.Len(); i++ {
		ai := idxs.At(i)
		if ai < 0 || int(ai) >= attrTable.Len() {
			continue
		}
		kv := attrTable.At(int(ai))
		if stringAt(stringTable, kv.KeyStrindex()) != profileFrameTypeKey {
			continue
		}
		return kv.Value().AsString()
	}
	return ""
}

func stringAt(stringTable pcommon.StringSlice, idx int32) string {
	if idx < 0 || int(idx) >= stringTable.Len() {
		return ""
	}
	return stringTable.At(int(idx))
}
