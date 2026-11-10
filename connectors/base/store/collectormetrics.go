package store

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectormetrics"
)

// The Collector's self-reported counters, on their own two columns of the managed_collectors row.
//
// Deliberately NOT part of collectorColumns and the positional row scan, even though they live on that
// row. Three reasons, and the first is the one that matters:
//
//   - They have exactly one writer (the OpAMP session) and one reader (the metrics query), and neither
//     is the row's other traffic. Threading a JSON blob through the scan every other reader performs
//     would make every report read and decode counters nobody asked for.
//   - The counter set follows the Collector's own internal telemetry, which this schema does not own; it
//     is a snapshot, not a set of columns to migrate in lockstep with the Collector.
//   - collectorColumns is checked positionally against migration 0012, and these arrived in 0013.
//
// The row must already exist: like every other Record* accessor, this one refuses to invent a Collector
// (see the note on Store.update).

// RecordCollectorMetrics stores the snapshot a Collector reported, and the time the COLLECTOR took it.
//
// observedAt is the Collector's own timestamp rather than now(), because the whole value of a cached
// snapshot is that a reader can tell how stale it is. Stamping it on arrival would report a snapshot
// taken minutes ago inside a queued message as current.
func (s *Store) RecordCollectorMetrics(ctx context.Context, key CollectorKey, snapshot collectormetrics.Snapshot, observedAt time.Time) error {
	raw, err := collectormetrics.Encode(snapshot)
	if err != nil {
		return err
	}
	return s.update(ctx, key, "metrics", `
		metrics = $3::jsonb,
		metrics_observed_at = $4`,
		string(raw), observedAt)
}

// CollectorMetrics returns the last snapshot this Collector reported.
//
// found=false means it has never reported one — which is NOT a snapshot of zeroes. A Collector nobody
// has heard from and a Collector that has handled nothing are different states, and collapsing them
// would put a confident row of zeroes in front of somebody debugging why no telemetry is arriving.
func (s *Store) CollectorMetrics(ctx context.Context, key CollectorKey) (collectormetrics.Record, bool, error) {
	var (
		raw        []byte
		observedAt *time.Time
	)
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT metrics, metrics_observed_at
		FROM %s.managed_collectors
		WHERE connector_name = $1 AND region = $2
	`, s.schema), s.connectorName, key.Region).Scan(&raw, &observedAt)
	if err != nil {
		if isNoRows(err) {
			return collectormetrics.Record{}, false, fmt.Errorf("%w: region %s", ErrCollectorNotFound, key.Region)
		}
		// A refresh poll can be canceled when its short wait window expires.
		// The caller handles that as a cached result, so it is not a store failure.
		if ctx.Err() != nil {
			return collectormetrics.Record{}, false, ctx.Err()
		}
		collectorsLog.Error("read collector metrics failed",
			"connector", s.connectorName, "region", key.Region, "error", err)
		return collectormetrics.Record{}, false, fmt.Errorf("reading the metrics for the Collector in region %s: %w", key.Region, err)
	}
	// The row exists but nothing has been reported into it yet.
	if observedAt == nil || len(raw) == 0 {
		return collectormetrics.Record{}, false, nil
	}

	snapshot, err := collectormetrics.Decode(raw)
	if err != nil {
		return collectormetrics.Record{}, false, err
	}
	return collectormetrics.Record{Snapshot: snapshot, ObservedAt: *observedAt}, true, nil
}
