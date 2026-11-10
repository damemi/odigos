package repo

import (
	"hash/fnv"
	"time"
)

// HashTransactionID derives a stable, non-negative id from the transaction's
// identity, so the same (service, namespace, operation, kind) maps to the same
// id on every replica with no DB round-trip or auto-increment.
func HashTransactionID(service, namespace, operation string, kind TransactionKind) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(service))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(operation))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	return int64(h.Sum64() >> 1) // drop the sign bit → always non-negative
}

// NewTransaction builds a Transaction with a deterministic id from its identity
// fields. Safe to call on the hot path - no DB.
func NewTransaction(service, namespace, operation string, kind TransactionKind) Transaction {
	return Transaction{
		ID:        HashTransactionID(service, namespace, operation, kind),
		Service:   service,
		Namespace: namespace,
		Operation: operation,
		Kind:      kind,
	}
}

// TransactionKind classifies the trigger that started the trace. The engine
// uses it to pick per-kind defaults and the debug UI uses it for filtering.
// A plain string in the DB, so adding a kind needs no migration.
type TransactionKind string

const (
	KindHTTP     TransactionKind = "HTTP"     // HTTP server endpoint
	KindGRPC     TransactionKind = "GRPC"     // gRPC server method
	KindConsumer TransactionKind = "CONSUMER" // message queue / pub-sub consumer
	KindProducer TransactionKind = "PRODUCER" // message queue publisher that starts a trace
	KindCron     TransactionKind = "CRON"     // scheduled job
	KindInternal TransactionKind = "INTERNAL" // background goroutine / startup task that starts its own trace
)

// Transaction is the unique unit of work baselined against - the identity of a
// trace's root span. Examples by Kind:
//
//	HTTP:     (user-service,   "POST /checkout")
//	GRPC:     (user-service,   "myapp.UserService/Login")
//	CONSUMER: (billing-worker, "kafka.consume orders")
//	CRON:     (billing-worker, "cron.nightly-rollup")
//	INTERNAL: (inventory-svc,  "internal.reindex")
//
// Shape variability within one transaction is captured by per-class
// BaselineClass rows, not by minting new transactions.
type Transaction struct {
	ID        int64
	Service   string
	Namespace string
	Operation string
	Kind      TransactionKind
}

// TransactionRef is a transaction's id and the service it belongs to, without
// the operation string. ListTransactionRefs returns these so the engine can
// seed its per-service admission index from the whole inventory cheaply.
type TransactionRef struct {
	ID        int64
	Namespace string
	Service   string
}

// TransactionStat is a Transaction plus its observed activity and baseline
// promotion state. Promoted is true only when every learned class has settled
// (graduated or saturated, see BaselineClass.Settled); HasBaseline
// distinguishes "still learning" from "no baseline yet". SaturatedClasses lists
// the classes that hit the per-set member cap and are not enforced.
type TransactionStat struct {
	Transaction
	Volume           int64
	LastSeen         time.Time
	HasBaseline      bool
	Promoted         bool
	SaturatedClasses []DeviationClass
}

// ServiceStat rolls transactions up to one (namespace, service): count, combined
// volume, and most recent activity. Returned by ListServiceStats for the debug
// UI's group-by-service overview.
type ServiceStat struct {
	Namespace        string
	Service          string
	TransactionCount int64
	Volume           int64
	LastSeen         time.Time
}

// ActivityDelta is one transaction's unflushed activity: volume since the last
// flush plus the latest observation time. Additive across replicas and flushes
// (volume sums, last_seen maxes), so it rides the engine's write-behind batch
// into RecordActivity.
type ActivityDelta struct {
	TransactionID int64
	Volume        int64
	LastSeen      time.Time
}
