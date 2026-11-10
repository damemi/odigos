package clustercollectorsgroup

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/odigos-io/odigos/common"
	commonlogger "github.com/odigos-io/odigos/common/logger"
)

var (
	errInvalidTraceAggregationValue = errors.New("invalid collectorGateway.traceAggregation value, using the default")

	// The collectors group is resolved on every reconcile; warn about the deprecated
	// field once per process instead of on each one.
	legacyWaitDurationWarned atomic.Bool
)

// resolveTraceAggregation validates collectorGateway.traceAggregation for the
// gateway's groupby processor. Invalid values are dropped so the processor default
// applies instead of the gateway failing to load its config. Returns nil when
// nothing valid is set.
func resolveTraceAggregation(logger *commonlogger.ContextLogger, odigosCfg *common.OdigosConfiguration) *common.TraceAggregationConfiguration {
	if legacyTraceAggregationWaitSet(odigosCfg) {
		if legacyWaitDurationWarned.CompareAndSwap(false, true) {
			logger.Info("sampling.tailSampling.traceAggregationWaitDuration is deprecated and ignored, set collectorGateway.traceAggregation.syncWait and asyncWait instead")
		}
	}
	if odigosCfg == nil || odigosCfg.CollectorGateway == nil || odigosCfg.CollectorGateway.TraceAggregation == nil {
		return nil
	}
	in := odigosCfg.CollectorGateway.TraceAggregation

	out := common.TraceAggregationConfiguration{
		SyncWait:   validDuration(logger, "syncWait", in.SyncWait, false),
		AsyncWait:  validDuration(logger, "asyncWait", in.AsyncWait, false),
		RootSettle: validDuration(logger, "rootSettle", in.RootSettle, true),
	}
	if in.MaxTraces != nil {
		if *in.MaxTraces > 0 {
			out.MaxTraces = in.MaxTraces
		} else {
			logger.Error(errInvalidTraceAggregationValue, "ignoring traceAggregation setting", "field", "maxTraces", "value", *in.MaxTraces)
		}
	}
	if in.LateSpansMaxMemoryMiB != nil {
		if *in.LateSpansMaxMemoryMiB >= 0 {
			out.LateSpansMaxMemoryMiB = in.LateSpansMaxMemoryMiB
		} else {
			logger.Error(errInvalidTraceAggregationValue, "ignoring traceAggregation setting", "field", "lateSpansMaxMemoryMiB", "value", *in.LateSpansMaxMemoryMiB)
		}
	}
	if out == (common.TraceAggregationConfiguration{}) {
		return nil
	}
	return &out
}

func legacyTraceAggregationWaitSet(odigosCfg *common.OdigosConfiguration) bool {
	if odigosCfg == nil || odigosCfg.Sampling == nil || odigosCfg.Sampling.TailSampling == nil {
		return false
	}
	//nolint:staticcheck // read only to warn that it is ignored
	return odigosCfg.Sampling.TailSampling.TraceAggregationWaitDuration != nil
}

func validDuration(logger *commonlogger.ContextLogger, field string, v *string, allowZero bool) *string {
	if v == nil || *v == "" {
		return nil
	}
	d, err := time.ParseDuration(*v)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		logger.Error(errInvalidTraceAggregationValue, "ignoring traceAggregation setting", "field", field, "value", *v)
		return nil
	}
	return v
}
