package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// InsertObservations stores traces as UI examples in one round trip. The
// caller (the engine's background sample writer) picks each sample_reason; the
// repo does not sample. A trace re-stored under the same reason replaces the
// previous copy.
func (r *Repo) InsertObservations(ctx context.Context, obs []repo.Observation) error {
	if len(obs) == 0 {
		return nil
	}
	sorted := append([]repo.Observation(nil), obs...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.TransactionID != b.TransactionID {
			return a.TransactionID < b.TransactionID
		}
		if a.SampleReason != b.SampleReason {
			return a.SampleReason < b.SampleReason
		}
		return a.TraceID < b.TraceID
	})
	now := time.Now()
	rows := make([][]any, 0, len(sorted))
	for _, o := range sorted {
		observedAt := o.ObservedAt
		if observedAt.IsZero() {
			observedAt = now
		}
		raw := o.RawOTLP
		if raw == nil {
			raw = []byte{}
		}
		rows = append(rows, []any{o.TransactionID, o.SampleReason, o.TraceID, observedAt, o.DurationUs, raw})
	}
	err := execBatch(ctx, r.pool,
		`INSERT INTO trace_observations (transaction_id, sample_reason, trace_id, observed_at, duration_us, raw_otlp)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (transaction_id, sample_reason, trace_id) DO UPDATE SET
			observed_at = EXCLUDED.observed_at, duration_us = EXCLUDED.duration_us, raw_otlp = EXCLUDED.raw_otlp`,
		rows)
	if err != nil {
		return fmt.Errorf("insert observations: %w", err)
	}
	return nil
}

// Meta-only columns for list paths. raw_otlp is intentionally omitted - blobs
// are loaded via GetObservationByTrace / GetLatestObservationByReason.
const observationMetaCols = `transaction_id, trace_id, observed_at, duration_us, sample_reason`

const observationCols = observationMetaCols + `, raw_otlp`

func scanObservationMeta(s rowScanner) (repo.Observation, error) {
	var o repo.Observation
	if err := s.Scan(&o.TransactionID, &o.TraceID, &o.ObservedAt, &o.DurationUs, &o.SampleReason); err != nil {
		return repo.Observation{}, err
	}
	return o, nil
}

func scanObservation(s rowScanner) (repo.Observation, error) {
	var o repo.Observation
	if err := s.Scan(&o.TransactionID, &o.TraceID, &o.ObservedAt, &o.DurationUs, &o.SampleReason, &o.RawOTLP); err != nil {
		return repo.Observation{}, err
	}
	return o, nil
}

// ListObservationsByTransaction returns the newest examples for one
// (transaction, sample_reason), newest first. With an empty sampleReason it
// returns every reason stored for the newest traces. Metadata only - RawOTLP is
// empty; fetch the blob with GetObservationByTrace when needed.
func (r *Repo) ListObservationsByTransaction(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	if limit <= 0 {
		limit = 50
	}
	var a args
	filter := "transaction_id = " + a.add(transactionID)
	if sampleReason != "" {
		filter += " AND sample_reason = " + a.add(sampleReason)
	}
	lim := a.add(limit)
	q := `SELECT ` + observationMetaCols + ` FROM trace_observations
		 WHERE ` + filter + `
		   AND trace_id IN (
			SELECT trace_id FROM trace_observations WHERE ` + filter + `
			GROUP BY trace_id ORDER BY max(observed_at) DESC LIMIT ` + lim + `)
		 ORDER BY observed_at DESC
		 LIMIT ` + lim
	return queryRows(ctx, r.pool, "list observations", q, scanObservationMeta, a...)
}

// GetObservationByTrace returns the stored trace (raw OTLP) for one
// (transaction, trace), ok=false when none was retained. Backs compare /
// accept / view-trace paths that need the blob.
func (r *Repo) GetObservationByTrace(ctx context.Context, transactionID int64, traceID string) (repo.Observation, bool, error) {
	return queryRow(ctx, r.pool, "get observation by trace",
		`SELECT `+observationCols+` FROM trace_observations
		 WHERE transaction_id = $1 AND trace_id = $2
		 ORDER BY observed_at DESC LIMIT 1`,
		scanObservation, transactionID, traceID)
}

// GetLatestObservationByReason returns the newest stored observation for a
// sample_reason across all transactions, ok=false when none was retained. Backs
// the guardrail evidence view.
func (r *Repo) GetLatestObservationByReason(ctx context.Context, sampleReason string) (repo.Observation, bool, error) {
	return queryRow(ctx, r.pool, "get observation by reason",
		`SELECT `+observationCols+` FROM trace_observations
		 WHERE sample_reason = $1
		 ORDER BY observed_at DESC LIMIT 1`,
		scanObservation, sampleReason)
}

// ListObservationsWithRaw returns the newest stored traces for one
// (transaction, sample_reason) including raw OTLP, newest first. Cold path only
// (the recommendation miner); list/UI paths use the meta-only variant.
func (r *Repo) ListObservationsWithRaw(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	if limit <= 0 {
		limit = 50
	}
	return queryRows(ctx, r.pool, "list observations with raw",
		`SELECT `+observationCols+` FROM trace_observations
		 WHERE transaction_id = $1 AND sample_reason = $2
		 ORDER BY observed_at DESC
		 LIMIT $3`,
		scanObservation, transactionID, sampleReason, limit)
}

// CountObservationsByReason returns how many distinct traces are stored for
// one transaction, per sample_reason.
func (r *Repo) CountObservationsByReason(ctx context.Context, transactionID int64) (map[string]int, error) {
	type reasonCount struct {
		reason string
		n      int64
	}
	rows, err := queryRows(ctx, r.pool, "count observations by reason",
		`SELECT sample_reason, count(*) FROM trace_observations WHERE transaction_id = $1 GROUP BY sample_reason`,
		func(s rowScanner) (reasonCount, error) {
			var rc reasonCount
			err := s.Scan(&rc.reason, &rc.n)
			return rc, err
		}, transactionID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, rc := range rows {
		out[rc.reason] = int(rc.n)
	}
	return out, nil
}
