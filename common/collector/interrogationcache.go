package collector

import "go.opentelemetry.io/collector/pdata/pcommon"

// InterrogationFrame is one stack frame from a profile sample (leaf-first order).
type InterrogationFrame struct {
	// Name is the human-readable function name, or mapping+offset / address fallback.
	Name string
	// FrameType is the profile.frame.type attribute (e.g. "jvm", "hotspot").
	// Empty when the location has no such attribute.
	FrameType string
}

// InterrogationSample is one profile sample linked to a span, with its resolved stack.
type InterrogationSample struct {
	// SampleType is the OTLP profile sample type: "events" (probe/uprobe) or
	// "samples" (periodic CPU). See ebpf-profiler TraceOriginProbe / TraceOriginSampling.
	SampleType string
	// Frames are ordered leaf-first (same order as profile location indices).
	Frames []InterrogationFrame
}

// InterrogationCacheExtension is implemented by the odigos_interrogation_cache collector
// extension. The interrogation profiles exporter stores probe/uprobe and periodic sample
// stacks keyed by span link; the traces exporter reads them for the bounding join.
type InterrogationCacheExtension interface {
	// RecordSample stores one profile sample's frames for the given trace and span.
	// Callers must pass non-empty trace and span IDs. Multiple samples for the same
	// key are retained as separate entries.
	RecordSample(traceID pcommon.TraceID, spanID pcommon.SpanID, sample InterrogationSample)

	// GetSamples returns the samples recorded for the given trace and span.
	// ok is false when no non-expired entry exists. The returned slices are copies
	// and safe to mutate.
	GetSamples(traceID pcommon.TraceID, spanID pcommon.SpanID) (samples []InterrogationSample, ok bool)
}
