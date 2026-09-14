package engine

import (
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// TransactionID is a transaction's natural identity, derived from a trace's
// root span: (service, namespace, operation, kind). Namespace keeps same-named
// services in different namespaces distinct.
type TransactionID struct {
	Service   string
	Namespace string
	Operation string
	Kind      repo.TransactionKind
}

// identifyTransaction derives the transaction identity from a trace's root span
// using OpenTelemetry semantic conventions, as called by Engine.processOne.
// Kind heuristics, in order:
//
//	rpc.system=grpc                 -> GRPC
//	SERVER                          -> HTTP
//	CONSUMER                        -> CONSUMER
//	PRODUCER                        -> PRODUCER
//	INTERNAL + faas.trigger=timer   -> CRON
//	INTERNAL (or unspecified)       -> INTERNAL
//
// rpc.system wins over the kind enum because some instrumentations emit RPC root
// spans with the wrong OTel kind (e.g. the Node.js gRPC client reports
// kind=INTERNAL while setting rpc.system=grpc); trusting the attribute keeps
// them out of INTERNAL. Returns ok=false when the root isn't a valid
// transaction and the caller should drop the trace (see discardableRoot).
func identifyTransaction(t *Trace) (TransactionID, bool) {
	root := t.Root
	if discardableRoot(root) {
		return TransactionID{}, false
	}
	id := TransactionID{
		Service:   root.Service,
		Namespace: namespaceFromRoot(root),
		Operation: root.Name,
		Kind:      kindFromRoot(root),
	}
	if id.Service == "" {
		id.Service = "unknown"
	}
	if id.Namespace == "" {
		id.Namespace = "unknown"
	}
	if id.Operation == "" {
		id.Operation = "unknown"
	}
	return id, true
}

// namespaceFromRoot resolves the workload namespace, preferring OTel
// service.namespace and falling back to the k8s namespace Odigos injects. Empty
// when neither is present (identifyTransaction then defaults it to "unknown").
func namespaceFromRoot(s *Span) string {
	return firstNonEmpty(s.Attr(semconv.ServiceNamespace), s.Attr(semconv.K8SNamespaceName))
}

// discardableRoot rejects traces whose root is broken context rather than a
// real workload transaction. The only case is an HTTP-shaped CLIENT root: an
// outbound client span surfacing as a root means the server parent didn't make
// it into this batch (or propagation broke). Baselining these would explode
// transaction cardinality (query strings in span names) and learn a service's
// outbound behaviour as if inbound, poisoning the real transaction's baselines.
//
// CLIENT roots carrying rpc.system are kept: RPC clients name spans after the
// method (low cardinality) and a standalone RPC client is a legitimate trace
// starter. SERVER/CONSUMER/PRODUCER/INTERNAL/UNSPECIFIED roots are all kept.
func discardableRoot(s *Span) bool {
	if s.Kind != tracepb.Span_SPAN_KIND_CLIENT {
		return false
	}
	// RPC clients are not the HTTP-cardinality problem; keep them.
	if s.Attr(semconv.RPCSystem) != "" {
		return false
	}
	return true
}

func kindFromRoot(s *Span) repo.TransactionKind {
	if s.Attr(semconv.RPCSystem) == semconv.RPCSystemGRPC {
		return repo.KindGRPC
	}
	switch s.Kind {
	case tracepb.Span_SPAN_KIND_SERVER:
		return repo.KindHTTP
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return repo.KindConsumer
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return repo.KindProducer
	case tracepb.Span_SPAN_KIND_INTERNAL:
		if s.Attr(semconv.FaaSTrigger) == semconv.FaaSTriggerTimer {
			return repo.KindCron
		}
		return repo.KindInternal
	default:
		return repo.KindInternal
	}
}
