// Package collectormetrics keeps the latest own-telemetry samples reported by managed Collectors.
package collectormetrics

import (
	"math"
	"sync"
	"time"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// InstanceSnapshot is a nullable view of one Collector instance's counters.
type InstanceSnapshot struct {
	Region, InstanceID                                      string
	ObservedAt                                              time.Time
	AcceptedSpans, AcceptedMetricPoints, AcceptedLogRecords *int64
	RefusedSpans, RefusedMetricPoints, RefusedLogRecords    *int64
	SentSpans, SentMetricPoints, SentLogRecords             *int64
	FailedSpans, FailedMetricPoints, FailedLogRecords       *int64
	QueueSize, QueueCapacity, MemoryBytes, UptimeSeconds    *int64
	CPUPercent                                              *float64
	cpuSeconds                                              *float64
}

// Cache is shared by the OpAMP OTLP receiver and the GraphQL coordinator.
// A regional Collector may have several running instances, each with its own counters.
type Cache struct {
	mu       sync.RWMutex
	byRegion map[string]map[string]InstanceSnapshot
}

func NewCache() *Cache { return &Cache{byRegion: make(map[string]map[string]InstanceSnapshot)} }

// Record replaces an instance's snapshot. An OTLP export is a periodic full set of own metrics.
func (c *Cache) Record(region, instanceID string, resources []*metricspb.ResourceMetrics, receivedAt time.Time) {
	if region == "" || instanceID == "" || len(instanceID) > 256 {
		return
	}
	s := InstanceSnapshot{Region: region, InstanceID: instanceID}
	for _, resource := range resources {
		for _, scope := range resource.GetScopeMetrics() {
			for _, m := range scope.GetMetrics() {
				value, observed, ok := metricValue(m)
				if !ok {
					continue
				}
				if observed.After(s.ObservedAt) {
					s.ObservedAt = observed
				}
				assign(&s, m.GetName(), value)
			}
		}
	}
	if s.ObservedAt.IsZero() || s.ObservedAt.After(receivedAt.Add(5*time.Minute)) {
		s.ObservedAt = receivedAt.UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byRegion[region] == nil {
		c.byRegion[region] = make(map[string]InstanceSnapshot)
	}
	for id, previous := range c.byRegion[region] {
		if receivedAt.Sub(previous.ObservedAt) > 10*time.Minute {
			delete(c.byRegion[region], id)
		}
	}
	if _, exists := c.byRegion[region][instanceID]; !exists && len(c.byRegion[region]) >= 32 {
		oldestID := ""
		oldestAt := time.Time{}
		for id, previous := range c.byRegion[region] {
			if oldestID == "" || previous.ObservedAt.Before(oldestAt) {
				oldestID, oldestAt = id, previous.ObservedAt
			}
		}
		if oldestID != "" {
			delete(c.byRegion[region], oldestID)
		}
	}
	if previous, ok := c.byRegion[region][instanceID]; ok {
		if previous.ObservedAt.After(s.ObservedAt) {
			return
		}
		if s.CPUPercent == nil && s.cpuSeconds != nil && previous.cpuSeconds != nil {
			elapsed := s.ObservedAt.Sub(previous.ObservedAt).Seconds()
			if elapsed > 0 && *s.cpuSeconds >= *previous.cpuSeconds {
				percent := 100 * (*s.cpuSeconds - *previous.cpuSeconds) / elapsed
				s.CPUPercent = &percent
			}
		}
	}
	c.byRegion[region][instanceID] = s
}

// Snapshot sums fresh instances. Stale instances from replaced tasks are excluded so a rollout
// cannot double-count the old task's cumulative counters indefinitely.
func (c *Cache) Snapshot(region string) (InstanceSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	instances := c.byRegion[region]
	if len(instances) == 0 {
		return InstanceSnapshot{}, false
	}
	latest := time.Time{}
	for _, s := range instances {
		if s.ObservedAt.After(latest) {
			latest = s.ObservedAt
		}
	}
	out := InstanceSnapshot{Region: region, ObservedAt: latest}
	for _, s := range instances {
		if latest.Sub(s.ObservedAt) > 2*time.Minute {
			continue
		}
		add(&out.AcceptedSpans, s.AcceptedSpans)
		add(&out.AcceptedMetricPoints, s.AcceptedMetricPoints)
		add(&out.AcceptedLogRecords, s.AcceptedLogRecords)
		add(&out.RefusedSpans, s.RefusedSpans)
		add(&out.RefusedMetricPoints, s.RefusedMetricPoints)
		add(&out.RefusedLogRecords, s.RefusedLogRecords)
		add(&out.SentSpans, s.SentSpans)
		add(&out.SentMetricPoints, s.SentMetricPoints)
		add(&out.SentLogRecords, s.SentLogRecords)
		add(&out.FailedSpans, s.FailedSpans)
		add(&out.FailedMetricPoints, s.FailedMetricPoints)
		add(&out.FailedLogRecords, s.FailedLogRecords)
		add(&out.QueueSize, s.QueueSize)
		add(&out.QueueCapacity, s.QueueCapacity)
		add(&out.MemoryBytes, s.MemoryBytes)
		add(&out.UptimeSeconds, s.UptimeSeconds)
		if s.CPUPercent != nil {
			if out.CPUPercent == nil {
				out.CPUPercent = new(float64)
			}
			*out.CPUPercent += *s.CPUPercent
		}
	}
	return out, true
}

// Stored converts the aggregated OTLP counters to the persisted GraphQL snapshot.
// GraphQL Int is 32-bit, so a larger cumulative counter is capped instead of wrapping.
func (s InstanceSnapshot) Stored() Snapshot {
	return Snapshot{
		AcceptedSpans: counterInt(s.AcceptedSpans), AcceptedMetricPoints: counterInt(s.AcceptedMetricPoints),
		AcceptedLogRecords: counterInt(s.AcceptedLogRecords), RefusedSpans: counterInt(s.RefusedSpans),
		RefusedMetricPoints: counterInt(s.RefusedMetricPoints), RefusedLogRecords: counterInt(s.RefusedLogRecords),
		SentSpans: counterInt(s.SentSpans), SentMetricPoints: counterInt(s.SentMetricPoints),
		SentLogRecords: counterInt(s.SentLogRecords), FailedSpans: counterInt(s.FailedSpans),
		FailedMetricPoints: counterInt(s.FailedMetricPoints), FailedLogRecords: counterInt(s.FailedLogRecords),
		QueueSize: counterInt(s.QueueSize), QueueCapacity: counterInt(s.QueueCapacity),
		CPUPercent: s.CPUPercent, MemoryBytes: counterInt(s.MemoryBytes), UptimeSeconds: counterInt(s.UptimeSeconds),
	}
}

func counterInt(v *int64) *int {
	if v == nil {
		return nil
	}
	n := *v
	if n > math.MaxInt32 {
		n = math.MaxInt32
	}
	i := int(n)
	return &i
}

func add(dst **int64, value *int64) {
	if value == nil {
		return
	}
	if *dst == nil {
		*dst = new(int64)
	}
	**dst += *value
}

func metricValue(m *metricspb.Metric) (float64, time.Time, bool) {
	var points []*metricspb.NumberDataPoint
	if sum := m.GetSum(); sum != nil {
		points = sum.GetDataPoints()
	}
	if gauge := m.GetGauge(); gauge != nil {
		points = gauge.GetDataPoints()
	}
	if len(points) == 0 {
		return 0, time.Time{}, false
	}
	var total float64
	var observed time.Time
	for _, p := range points {
		if p == nil {
			continue
		}
		v := p.GetAsDouble()
		if _, ok := p.GetValue().(*metricspb.NumberDataPoint_AsInt); ok {
			v = float64(p.GetAsInt())
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		total += v
		if p.GetTimeUnixNano() != 0 {
			at := time.Unix(0, int64(p.GetTimeUnixNano())).UTC()
			if at.After(observed) {
				observed = at
			}
		}
	}
	return total, observed, true
}

func assign(s *InstanceSnapshot, name string, value float64) {
	if name == "process_cpu_utilization" {
		percent := value * 100
		s.CPUPercent = &percent
		return
	}
	if name == "otelcol_process_cpu_seconds" || name == "otelcol_process_cpu_seconds_total" || name == "process_cpu_time" {
		s.cpuSeconds = &value
		return
	}
	if value < 0 || value > math.MaxInt64 {
		return
	}
	v := int64(value)
	switch name {
	case "otelcol_receiver_accepted_spans":
		add(&s.AcceptedSpans, &v)
	case "otelcol_receiver_accepted_metric_points":
		add(&s.AcceptedMetricPoints, &v)
	case "otelcol_receiver_accepted_log_records":
		add(&s.AcceptedLogRecords, &v)
	case "otelcol_receiver_refused_spans":
		add(&s.RefusedSpans, &v)
	case "otelcol_receiver_refused_metric_points":
		add(&s.RefusedMetricPoints, &v)
	case "otelcol_receiver_refused_log_records":
		add(&s.RefusedLogRecords, &v)
	case "otelcol_exporter_sent_spans":
		add(&s.SentSpans, &v)
	case "otelcol_exporter_sent_metric_points":
		add(&s.SentMetricPoints, &v)
	case "otelcol_exporter_sent_log_records":
		add(&s.SentLogRecords, &v)
	case "otelcol_exporter_send_failed_spans":
		add(&s.FailedSpans, &v)
	case "otelcol_exporter_send_failed_metric_points":
		add(&s.FailedMetricPoints, &v)
	case "otelcol_exporter_send_failed_log_records":
		add(&s.FailedLogRecords, &v)
	case "otelcol_exporter_queue_size":
		add(&s.QueueSize, &v)
	case "otelcol_exporter_queue_capacity":
		add(&s.QueueCapacity, &v)
	case "otelcol_process_memory_rss", "process_memory_usage":
		add(&s.MemoryBytes, &v)
	case "otelcol_process_uptime", "otelcol_process_uptime_seconds_total", "process_uptime":
		add(&s.UptimeSeconds, &v)
	}
}
