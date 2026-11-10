package engine

import (
	"encoding/hex"
	"strconv"
	"time"

	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const javaEbpfDistroName = "java-ebpf-instrumentations"

const k8SArgoRolloutNameAttribute = "k8s.argoproj.rollout.name"

var interrogationWorkloadAttrs = []struct {
	key  string
	kind string
}{
	{key: "k8s.deployment.name", kind: "Deployment"},
	{key: "k8s.statefulset.name", kind: "StatefulSet"},
	{key: "k8s.daemonset.name", kind: "DaemonSet"},
	{key: "k8s.cronjob.name", kind: "CronJob"},
	{key: k8SArgoRolloutNameAttribute, kind: "Rollout"},
}

// SetInterrogation installs the optional Redis call-trie correlator.
// Nil is a no-op (correlation stays off). Called from main after Open when
// INTERROGATION_REDIS_ENDPOINT is set (interrogation.enabled).
func (e *Engine) SetInterrogation(c *interrogation.Client) {
	if e == nil {
		return
	}
	e.interrogation = c
}

// enqueueInterrogation queues java-ebpf service entries under t for Redis
// call-trie correlation. Transaction inventory is the insights transactions
// table (resource_attributes), written on the baseline flush. Runs after
// processOne so only identities the per-service cap admitted get a trie: a
// dropped identity never reaches the inventory, so its trie would never be
// analyzed. Off the scoring hot path: Record is non-blocking; Redis runs on
// the batcher flush.
func (e *Engine) enqueueInterrogation(t *Trace) {
	if e == nil || e.interrogation == nil || t == nil || t.Root == nil {
		return
	}
	var dims []repo.TransactionIdentityDimension
	if e.system != nil {
		dims = e.system.snapshot().settings.TransactionIdentityDimensions
	}
	batch, ok := interrogationBatchFromTrace(t, dims, e.admission.contains)
	if !ok {
		return
	}
	e.interrogation.Record(batch)
}

// interrogationBatchFromTrace builds the Redis correlation batch for t. admitted,
// when non-nil, filters out identities the inventory does not hold.
func interrogationBatchFromTrace(t *Trace, dims []repo.TransactionIdentityDimension, admitted func(serviceKey string, id int64) bool) (interrogation.TraceBatch, bool) {
	rawTraceID, err := hex.DecodeString(t.TraceID)
	if err != nil || len(rawTraceID) == 0 {
		return interrogation.TraceBatch{}, false
	}

	entries := serviceEntrySpans(t)
	txs := make([]interrogation.TraceTransaction, 0, len(entries))
	for _, entry := range entries {
		if !isJavaEbpfInstrumentation(entry) {
			continue
		}
		wl, ok := workloadIdentityFromSpan(entry)
		if !ok {
			continue
		}
		spanIDs := sameWorkloadSpanIDs(entry)
		if len(spanIDs) == 0 {
			continue
		}
		ident, ok := identifyTransaction(&Trace{Root: entry})
		if !ok {
			continue
		}
		ident.Operation = applyIdentityDimensions(ident.Operation, entry, dims)
		tx := repo.NewTransaction(ident.Service, ident.Namespace, ident.Operation, ident.Kind)
		if admitted != nil && !admitted(repo.ServiceScopeKey(ident.Namespace, ident.Service), tx.ID) {
			continue
		}
		ts := time.Unix(0, int64(entry.StartUnix)).UTC()
		if entry.StartUnix == 0 {
			ts = time.Now().UTC()
		}
		txs = append(txs, interrogation.TraceTransaction{
			Timestamp:     ts,
			Namespace:     wl.namespace,
			WorkloadKind:  wl.kind,
			WorkloadName:  wl.name,
			ContainerName: wl.container,
			TransactionID: strconv.FormatInt(tx.ID, 10),
			RootSpanName:  entry.Name,
			RootSpanKind:  entry.Kind.String(),
			SpanIDs:       spanIDs,
		})
	}
	if len(txs) == 0 {
		return interrogation.TraceBatch{}, false
	}
	return interrogation.TraceBatch{
		TraceID:      string(rawTraceID),
		Transactions: txs,
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
	attrs := sourceResourceAttributes(s.ResourceAttrs)
	w := workloadIdentity{
		namespace: attrs[semconv.K8SNamespaceName],
		kind:      attrs[consts.OdigosWorkloadKindAttribute],
		name:      attrs[consts.OdigosWorkloadNameAttribute],
		container: attrs[semconv.K8SContainerName],
	}
	return w, w.complete()
}

func workloadKindAndName(attrs map[string]string) (string, string) {
	if kind := attrs[consts.OdigosWorkloadKindAttribute]; kind != "" {
		if name := attrs[consts.OdigosWorkloadNameAttribute]; name != "" {
			return kind, name
		}
	}
	for _, pair := range interrogationWorkloadAttrs {
		if name := attrs[pair.key]; name != "" {
			return pair.kind, name
		}
	}
	return "", ""
}

// sameWorkloadSpanIDs returns the entry plus same-workload descendants as raw
// span-id byte strings (exporter FixedString(8) form) for Redis stack matching.
func sameWorkloadSpanIDs(entry *Span) []string {
	if entry == nil {
		return nil
	}
	var out []string
	var walk func(s *Span)
	walk = func(s *Span) {
		if s == nil {
			return
		}
		if sameWorkloadSpan(s, entry) {
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
func sameWorkloadSpan(a, b *Span) bool {
	if a == nil || b == nil {
		return false
	}
	sa, sb := sourceResourceAttributes(a.ResourceAttrs), sourceResourceAttributes(b.ResourceAttrs)
	if len(sa) > 0 && len(sb) > 0 {
		return sourceAttributesEqual(sa, sb)
	}
	return a.Service != "" && a.Service == b.Service
}
