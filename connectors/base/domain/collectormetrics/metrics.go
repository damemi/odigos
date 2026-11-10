// Package collectormetrics is one Collector's self-reported counters: the snapshot type, its JSON
// encoding for the managed_collectors row, and the conversion to the agents-api model.
//
// Pure — no I/O. The OpAMP session writes a snapshot through store.RecordCollectorMetrics; the GraphQL
// managedCollectorMetrics query reads the last one back.
//
// Every counter is a pointer, and that is the whole design. A Collector that has never connected has
// reported nothing, and a zero would say "it has handled no spans" — which is a claim about a running
// Collector, not about one nobody has heard from. The two have to stay distinguishable all the way to
// the client, so the absence survives the store, this type, and the wire.
package collectormetrics

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"
)

// Snapshot is the counter set as the Collector reported it, at the moment IT took them.
//
// The json tags are the persisted shape (managed_collectors.metrics) and are therefore a compatibility
// surface: rename one and older rows stop decoding into it. Adding a field is safe — an older row simply
// leaves it nil, which reads correctly as "this Collector did not report that counter".
type Snapshot struct {
	// Receiver side: accepted is what came in, refused is what was turned away (backpressure, or a
	// payload the Collector would not take).
	AcceptedSpans        *int `json:"acceptedSpans,omitempty"`
	AcceptedMetricPoints *int `json:"acceptedMetricPoints,omitempty"`
	AcceptedLogRecords   *int `json:"acceptedLogRecords,omitempty"`
	RefusedSpans         *int `json:"refusedSpans,omitempty"`
	RefusedMetricPoints  *int `json:"refusedMetricPoints,omitempty"`
	RefusedLogRecords    *int `json:"refusedLogRecords,omitempty"`

	// Exporter side. Failed is the number that means telemetry was LOST — it is reported separately from
	// refused because the remedies are opposite ends of the pipeline: refused is the Collector protecting
	// itself, failed is the destination not taking the data.
	SentSpans          *int `json:"sentSpans,omitempty"`
	SentMetricPoints   *int `json:"sentMetricPoints,omitempty"`
	SentLogRecords     *int `json:"sentLogRecords,omitempty"`
	FailedSpans        *int `json:"failedSpans,omitempty"`
	FailedMetricPoints *int `json:"failedMetricPoints,omitempty"`
	FailedLogRecords   *int `json:"failedLogRecords,omitempty"`

	// Queue depth against capacity. Reported as a pair because depth alone says nothing: 5000 queued is
	// healthy at a capacity of 100000 and about to drop at a capacity of 5000.
	QueueSize     *int `json:"queueSize,omitempty"`
	QueueCapacity *int `json:"queueCapacity,omitempty"`

	CPUPercent    *float64 `json:"cpuPercent,omitempty"`
	MemoryBytes   *int     `json:"memoryBytes,omitempty"`
	UptimeSeconds *int     `json:"uptimeSeconds,omitempty"`
}

// Record is a snapshot plus when it was taken and how it was obtained.
type Record struct {
	Snapshot Snapshot
	// ObservedAt is when the COLLECTOR took the counters, not when they were stored or read. It is what
	// lets a reader judge whether a cached answer is current, which is the only reason a cached answer is
	// safe to serve at all.
	ObservedAt time.Time
	// Live says a new own-metrics report arrived while the query waited, rather than being read from
	// the stored snapshot. It is not inferred from ObservedAt, which may be recent for a cached answer.
	Live bool
}

// Encode renders a snapshot for the jsonb column.
func Encode(s Snapshot) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encoding the Collector metrics snapshot: %w", err)
	}
	return raw, nil
}

// Decode reads a snapshot back. An empty column is not an error — it is a Collector that has never
// reported — so it yields the zero Snapshot, whose every counter is correctly absent.
func Decode(raw []byte) (Snapshot, error) {
	var s Snapshot
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Snapshot{}, fmt.Errorf("reading the stored Collector metrics snapshot: %w", err)
	}
	return s, nil
}

// ToModel converts a record to the wire type.
func ToModel(region string, rec Record) *model.ManagedCollectorMetrics {
	s := rec.Snapshot
	return &model.ManagedCollectorMetrics{
		Region:     region,
		ObservedAt: rec.ObservedAt.UTC().Format(time.RFC3339),
		Live:       rec.Live,

		AcceptedSpans:        s.AcceptedSpans,
		AcceptedMetricPoints: s.AcceptedMetricPoints,
		AcceptedLogRecords:   s.AcceptedLogRecords,
		RefusedSpans:         s.RefusedSpans,
		RefusedMetricPoints:  s.RefusedMetricPoints,
		RefusedLogRecords:    s.RefusedLogRecords,
		SentSpans:            s.SentSpans,
		SentMetricPoints:     s.SentMetricPoints,
		SentLogRecords:       s.SentLogRecords,
		FailedSpans:          s.FailedSpans,
		FailedMetricPoints:   s.FailedMetricPoints,
		FailedLogRecords:     s.FailedLogRecords,

		QueueSize:     s.QueueSize,
		QueueCapacity: s.QueueCapacity,

		CPUPercent:    s.CPUPercent,
		MemoryBytes:   s.MemoryBytes,
		UptimeSeconds: s.UptimeSeconds,
	}
}
