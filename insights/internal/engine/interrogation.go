package engine

import (
	"encoding/hex"
	"maps"
	"strconv"
	"time"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const javaEbpfDistroName = "java-ebpf-instrumentations"

// SetInterrogation installs the optional Redis call-trie correlator.
// Nil is a no-op (correlation stays off). Called from main after Open when
// INTERROGATION_REDIS_ENDPOINT is set (interrogation.enabled).
func (e *Engine) SetInterrogation(c *interrogation.Client) {
	if e == nil {
		return
	}
	e.interrogation = c
}

// enqueueInterrogation queues one sub-transaction for Redis call-trie
// correlation, as called by Process for each sub-trace processOne resolved and
// admitted. tx is processOne's transaction, so the trie's transaction id is the
// inventory's (source_identity, written on the baseline flush) and a dropped
// identity never gets a trie it could not be analyzed under. Off the scoring
// hot path: Record is non-blocking and the batcher merges a trace's
// sub-transactions back into one batch; Redis runs on the batcher flush.
// rawTraceID is the trace's raw id bytes, from interrogationTraceID.
func (e *Engine) enqueueInterrogation(rawTraceID string, sub *Trace, tx repo.Transaction) {
	if e == nil || e.interrogation == nil || rawTraceID == "" || sub == nil || sub.Root == nil {
		return
	}
	itx, ok := interrogationTransaction(sub.Root, tx, e.sourceIdentity)
	if !ok {
		return
	}
	e.interrogation.Record(interrogation.TraceBatch{
		TraceID:      rawTraceID,
		Transactions: []interrogation.TraceTransaction{itx},
	})
}

// interrogationTraceID returns t's raw trace id bytes for the Redis profile
// key, decoded once per trace by Process. Empty when interrogation is off or
// the id doesn't decode, which skips interrogation for the whole trace.
func (e *Engine) interrogationTraceID(t *Trace) string {
	if e == nil || e.interrogation == nil {
		return ""
	}
	raw, err := hex.DecodeString(t.TraceID)
	if err != nil {
		return ""
	}
	return string(raw)
}

// interrogationTransaction builds the correlation entry for one sub-transaction
// rooted at entry. Only java-ebpf entries with a complete k8s workload identity
// and at least one same-workload span qualify. sourceIdentity must be the
// engine's, so same-workload matching agrees with the transaction identity.
func interrogationTransaction(entry *Span, tx repo.Transaction, sourceIdentity SourceIdentityFunc) (interrogation.TraceTransaction, bool) {
	if !isJavaEbpfInstrumentation(entry) {
		return interrogation.TraceTransaction{}, false
	}
	wl, ok := workloadIdentityFromSpan(entry)
	if !ok {
		return interrogation.TraceTransaction{}, false
	}
	spanIDs := sameWorkloadSpanIDs(entry, sourceIdentity)
	if len(spanIDs) == 0 {
		return interrogation.TraceTransaction{}, false
	}
	ts := time.Unix(0, int64(entry.StartUnix)).UTC()
	if entry.StartUnix == 0 {
		ts = time.Now().UTC()
	}
	return interrogation.TraceTransaction{
		Timestamp:     ts,
		Namespace:     wl.namespace,
		WorkloadKind:  wl.kind,
		WorkloadName:  wl.name,
		ContainerName: wl.container,
		TransactionID: strconv.FormatInt(tx.ID, 10),
		RootSpanName:  entry.Name,
		RootSpanKind:  entry.Kind.String(),
		SpanIDs:       spanIDs,
	}, true
}

func isJavaEbpfInstrumentation(s *Span) bool {
	return s != nil && s.Attr(semconv.TelemetryDistroName) == javaEbpfDistroName
}

type workloadIdentity struct {
	namespace string
	kind      string
	name      string
	container string
}

func (w workloadIdentity) complete() bool {
	return w.namespace != "" && w.kind != "" && w.name != "" && w.container != ""
}

func workloadIdentityFromSpan(s *Span) (workloadIdentity, bool) {
	if s == nil {
		return workloadIdentity{}, false
	}
	// The trie is keyed by k8s workload columns, so this is the k8s source
	// identity regardless of the engine's SourceIdentityFunc.
	attrs := sourceidentity.FromK8sResourceAttributesMap(s.ResourceAttrs)
	kind, name := workloadKindAndName(attrs)
	w := workloadIdentity{
		namespace: attrs[semconv.K8SNamespaceName],
		kind:      kind,
		name:      name,
		container: attrs[semconv.K8SContainerName],
	}
	return w, w.complete()
}

// workloadKindAndName reads the workload kind and name from a k8s source
// identity: the Odigos workload attributes when present, otherwise the kind
// matching whichever k8s workload name attribute it holds.
func workloadKindAndName(attrs map[string]string) (string, string) {
	if kind, name := attrs[consts.OdigosWorkloadKindAttribute], attrs[consts.OdigosWorkloadNameAttribute]; kind != "" && name != "" {
		return kind, name
	}
	for _, attr := range sourceidentity.K8sWorkloadNameAttributes {
		if name := attrs[attr.Key]; name != "" {
			return attr.Kind, name
		}
	}
	return "", ""
}

// sameWorkloadSpanIDs returns the entry plus same-workload descendants as raw
// span-id byte strings (exporter FixedString(8) form) for Redis stack matching.
func sameWorkloadSpanIDs(entry *Span, sourceIdentity SourceIdentityFunc) []string {
	if entry == nil {
		return nil
	}
	var out []string
	var walk func(s *Span)
	walk = func(s *Span) {
		if s == nil {
			return
		}
		if sameWorkloadSpan(s, entry, sourceIdentity) {
			if raw, err := hex.DecodeString(s.SpanID); err == nil && len(raw) > 0 {
				out = append(out, string(raw))
			}
		}
		for _, c := range s.Children {
			walk(c)
		}
	}
	walk(entry)
	return out
}

// sameWorkloadSpan compares source identities when both spans have one, so two
// workloads sharing a service.name stay apart. service.name is the fallback
// only when either side lacks a source identity.
func sameWorkloadSpan(a, b *Span, sourceIdentity SourceIdentityFunc) bool {
	if a == nil || b == nil {
		return false
	}
	sa, sb := sourceIdentity(a.ResourceAttrs), sourceIdentity(b.ResourceAttrs)
	if len(sa) > 0 && len(sb) > 0 {
		return maps.Equal(sa, sb)
	}
	return a.Service != "" && a.Service == b.Service
}
