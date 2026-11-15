package repo

import (
	"encoding/json"
	"time"
)

// AnomalyIssue is one deduped anomaly finding: every firing sharing a signature
// (transaction + triggered classes + offending items) rolled up into one row
// with an occurrence count and a sticky Status. List paths leave Evidence empty;
// GetAnomalyIssue loads it for the investigate view.
type AnomalyIssue struct {
	TransactionID    int64
	Signature        string
	Service          string
	Namespace        string
	Operation        string
	Kind             TransactionKind
	TriggeredClasses []DeviationClass
	Offending        string
	Evidence         json.RawMessage // empty on list; set on GetAnomalyIssue
	PolicyID         int64
	Occurrences      int64
	MaxScore         int
	FirstSeen        time.Time
	LastSeen         time.Time
	LastTraceID      string
	Status           AnomalyIssueStatus
}

// AnomalyOccurrence is a flush batch's worth of one signature's firings. The
// engine buffers per-trace occurrences and flushes them as summed increments
// via RecordAnomalyOccurrences, keeping a repeating deviation one issue with a
// rising count rather than one stored row per trace.
type AnomalyOccurrence struct {
	TransactionID    int64
	Signature        string
	TriggeredClasses []DeviationClass
	Offending        string
	Evidence         json.RawMessage
	PolicyID         int64
	Occurrences      int64
	MaxScore         int
	FirstSeen        time.Time
	LastSeen         time.Time
	LastTraceID      string
}

// AnomalyIssueStatus is where an issue sits in its lifecycle, derived from its
// max-merged decision timestamps. It buckets the Findings feed's anomaly tabs.
type AnomalyIssueStatus string

const (
	AnomalyIssueOpen AnomalyIssueStatus = "open"
	// AnomalyIssueAccepted: the operator confirmed the flagged behaviour is
	// legitimate and folded it into the baseline, so it stops alerting.
	AnomalyIssueAccepted  AnomalyIssueStatus = "accepted"
	AnomalyIssueDismissed AnomalyIssueStatus = "dismissed"
)

// AnomalyResolution selects which decision timestamp ResolveAnomalyIssue stamps,
// and thus the tab the issue moves to.
type AnomalyResolution string

const (
	AnomalyResolveAccept  AnomalyResolution = "accept"  // → AnomalyIssueAccepted
	AnomalyResolveDismiss AnomalyResolution = "dismiss" // → AnomalyIssueDismissed
	AnomalyResolveReopen  AnomalyResolution = "reopen"  // dismissed → AnomalyIssueOpen (re-triage)
)

// AnomalySampleReason builds the sample_reason tag ("anomaly:<signature>")
// stored on an anomaly's sample traces, so readers can fetch recent examples
// via ListObservationsByTransaction. Shared by the engine (writer) and the API
// (reader) so they agree on the format.
func AnomalySampleReason(signature string) string {
	return "anomaly:" + signature
}

const (
	// ExampleSampleReason tags representative normal-traffic traces kept while a
	// transaction is still learning, so the UI has examples to render. Collection
	// stops once every class is promoted, freezing the set for the compare view.
	ExampleSampleReason = "example"
)
