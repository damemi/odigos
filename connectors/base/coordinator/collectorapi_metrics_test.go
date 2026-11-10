package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectormetrics"
)

func TestWaitForNextCollectorMetricsMarksOnlyNewReportLive(t *testing.T) {
	baseline := collectormetrics.Record{ObservedAt: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)}
	newer := collectormetrics.Record{ObservedAt: baseline.ObservedAt.Add(time.Second)}
	got, found, err := waitForNextCollectorMetrics(context.Background(), baseline, true,
		100*time.Millisecond, time.Millisecond,
		func(context.Context) (collectormetrics.Record, bool, error) { return newer, true, nil })
	if err != nil || !found || !got.Live || !got.ObservedAt.Equal(newer.ObservedAt) {
		t.Fatalf("new report = %+v, found %t, error %v", got, found, err)
	}
}

func TestWaitForNextCollectorMetricsFallsBackToCache(t *testing.T) {
	baseline := collectormetrics.Record{ObservedAt: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)}
	got, found, err := waitForNextCollectorMetrics(context.Background(), baseline, true,
		10*time.Millisecond, time.Millisecond,
		func(context.Context) (collectormetrics.Record, bool, error) { return baseline, true, nil })
	if err != nil || !found || got.Live || !got.ObservedAt.Equal(baseline.ObservedAt) {
		t.Fatalf("cached report = %+v, found %t, error %v", got, found, err)
	}
}

func TestWaitForNextCollectorMetricsPropagatesReadFailure(t *testing.T) {
	want := errors.New("metrics store unavailable")
	_, _, err := waitForNextCollectorMetrics(context.Background(), collectormetrics.Record{}, false,
		100*time.Millisecond, time.Millisecond,
		func(context.Context) (collectormetrics.Record, bool, error) {
			return collectormetrics.Record{}, false, want
		})
	if !errors.Is(err, want) {
		t.Fatalf("read error = %v, want %v", err, want)
	}
}
