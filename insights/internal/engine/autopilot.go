package engine

import (
	"strconv"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"
	"github.com/odigos-io/odigos/insights/internal/autopilot"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const javaEbpfDistroName = "java-ebpf-instrumentations"

// SetAutopilot installs the optional Redis call-tree correlator.
// Nil is a no-op (correlation stays off). Called from main after Open when
// AUTOPILOT_ENABLED is set (insights.autopilot.enabled).
func (e *Engine) SetAutopilot(c *autopilot.Client) {
	if e == nil {
		return
	}
	e.autopilot = c
}

// recordAutopilot queues one trace's service invocations for Redis call-tree
// correlation, once per trace from Process. Each invocation is built from a
// sub-trace processOne resolved and admitted, so the call tree's transaction id
// is the inventory's (source_identity, written on the baseline flush) and a
// dropped identity never gets a call tree it could not be analyzed under. Off
// the scoring hot path: Record is non-blocking, and Redis runs on the batcher
// flush.
func (e *Engine) recordAutopilot(traceID string, invocations []autopilot.ServiceInvocation) {
	if e == nil || e.autopilot == nil || traceID == "" || len(invocations) == 0 {
		return
	}
	e.autopilot.Record(autopilot.TraceBatch{
		TraceID:            traceID,
		ServiceInvocations: invocations,
	})
}

// autopilotServiceInvocation builds the correlation entry for the sub-transaction
// sub. Only java-ebpf entries whose transaction has a complete k8s source
// identity qualify. Its spans are the entry's own invocation, so stacks of
// downstream services or of later calls back into the same service stay out.
func autopilotServiceInvocation(sub *Trace, tx repo.Transaction) (autopilot.ServiceInvocation, bool) {
	entry := sub.Root
	if !isJavaEbpfInstrumentation(entry) {
		return autopilot.ServiceInvocation{}, false
	}
	// Autopilot instruments k8s workload containers only.
	if _, err := sourceidentity.K8sSourceKeyFromResourceAttributesMap(tx.SourceIdentity); err != nil {
		return autopilot.ServiceInvocation{}, false
	}
	spanIDs := serviceInvocationSpanIDs(sub)
	if len(spanIDs) == 0 {
		return autopilot.ServiceInvocation{}, false
	}
	return autopilot.ServiceInvocation{
		SourceIdentity: tx.SourceIdentity,
		TransactionID:  strconv.FormatInt(tx.ID, 10),
		SpanIDs:        spanIDs,
		Measures:       serviceInvocationMeasures(sub),
	}, true
}

func isJavaEbpfInstrumentation(s *Span) bool {
	return s != nil && s.Attr(semconv.TelemetryDistroName) == javaEbpfDistroName
}

// serviceInvocationSpanIDs returns the hex span ids of sub's own service
// invocation, for Redis stack matching.
func serviceInvocationSpanIDs(sub *Trace) []string {
	out := make([]string, 0, len(sub.ServiceInvocationSpans))
	for _, s := range sub.ServiceInvocationSpans {
		if s.SpanID != "" {
			out = append(out, s.SpanID)
		}
	}
	return out
}

// serviceInvocationMeasures returns the timings of every probe span in sub's
// own service invocation and, last, of its entry span.
func serviceInvocationMeasures(sub *Trace) []measure.Sample {
	entry := sub.Root
	var measures []measure.Sample
	for _, s := range sub.ServiceInvocationSpans {
		if site := d4SiteOf(s); site != "" && s != entry {
			measures = append(measures, spanSample(s, site))
		}
	}
	root := spanSample(entry, "")
	root.Err = entry.StatusCode == tracepb.Status_STATUS_CODE_ERROR || httpStatus(entry) >= 500
	root.Spans = int64(len(sub.ServiceInvocationSpans))
	return append(measures, root)
}

// spanSample is a span's duration and the part of it outside its direct
// children. Only the status message of an errored span is kept: argument and
// return values can hold secrets.
func spanSample(s *Span, site string) measure.Sample {
	dur := spanMicros(s)
	children := int64(0)
	for _, c := range s.Children {
		children += spanMicros(c)
	}
	sm := measure.Sample{
		Bucket: int64(s.StartUnix) / int64(time.Second) / measure.BucketSeconds * measure.BucketSeconds,
		Site:   site,
		DurUs:  dur,
		SelfUs: max(dur-children, 0),
	}
	if s.StatusCode == tracepb.Status_STATUS_CODE_ERROR {
		sm.Err, sm.Msg = true, s.StatusMessage
	}
	return sm
}

func spanMicros(s *Span) int64 {
	if s.EndUnix <= s.StartUnix {
		return 0
	}
	return int64((s.EndUnix - s.StartUnix) / uint64(time.Microsecond))
}

func httpStatus(s *Span) int {
	code, _ := strconv.Atoi(s.Attr(semconv.HTTPResponseStatusCode))
	return code
}
