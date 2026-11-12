package repo

import "time"

// Observation is a single trace kept as a debug UI example for a transaction.
// RawOTLP holds the trace's spans as verbatim OTLP protobuf bytes when loaded
// via GetObservationByTrace / GetLatestObservationByReason. List paths leave
// it empty so ranking/listing never materializes large blobs.
type Observation struct {
	TraceID       string
	TransactionID int64
	ObservedAt    time.Time
	DurationUs    int64
	SampleReason  string // "example" | "anomaly_evidence"
	RawOTLP       []byte // empty on list; set on detail fetches
}
