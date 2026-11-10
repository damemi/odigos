package collectormetrics

import (
	"testing"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func reported(name string, value int64, at time.Time) []*metricspb.ResourceMetrics {
	return []*metricspb.ResourceMetrics{{ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
		Name: name,
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{{
			TimeUnixNano: uint64(at.UnixNano()), Value: &metricspb.NumberDataPoint_AsInt{AsInt: value},
		}}}},
	}}}}}}
}

func TestRegionalSnapshotReplacesPerInstanceAndExcludesOldTasks(t *testing.T) {
	cache := NewCache()
	now := time.Now().UTC()
	cache.Record("east", "task-a", reported("otelcol_receiver_accepted_spans", 10, now.Add(-3*time.Minute)), now.Add(-3*time.Minute))
	cache.Record("east", "task-b", reported("otelcol_receiver_accepted_spans", 7, now), now)
	s, ok := cache.Snapshot("east")
	if !ok || s.AcceptedSpans == nil || *s.AcceptedSpans != 7 {
		t.Fatalf("replaced task was counted: %+v, present %t", s, ok)
	}
	cache.Record("east", "task-b", reported("otelcol_receiver_accepted_spans", 9, now.Add(time.Second)), now.Add(time.Second))
	s, _ = cache.Snapshot("east")
	if s.AcceptedSpans == nil || *s.AcceptedSpans != 9 {
		t.Fatalf("cumulative counter was summed with its previous sample: %+v", s)
	}
	if _, ok := cache.Snapshot("west"); ok {
		t.Fatal("unreported region returned metrics")
	}
}

func TestCPUPercentUsesConsecutiveProcessSamples(t *testing.T) {
	cache := NewCache()
	start := time.Now().UTC()
	cache.Record("east", "task-a", reported("otelcol_process_cpu_seconds", 10, start), start)
	cache.Record("east", "task-a", reported("otelcol_process_cpu_seconds", 11, start.Add(10*time.Second)), start.Add(10*time.Second))
	s, ok := cache.Snapshot("east")
	if !ok || s.CPUPercent == nil || *s.CPUPercent != 10 {
		t.Fatalf("CPU percent = %+v, want 10", s.CPUPercent)
	}
}
