package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// InsertObservations stores traces as UI examples in one insert. The
// caller (the engine's background sample writer) picks each sample_reason; the
// repo does not sample.
func (r *Repo) InsertObservations(ctx context.Context, obs []repo.Observation) error {
	if len(obs) == 0 {
		return nil
	}
	now := time.Now()
	err := r.insert(ctx,
		"INSERT INTO trace_observations (transaction_id, trace_id, observed_at, duration_us, sample_reason, raw_otlp)",
		func(b driver.Batch) error {
			for _, o := range obs {
				observedAt := o.ObservedAt
				if observedAt.IsZero() {
					observedAt = now
				}
				if err := b.Append(o.TransactionID, o.TraceID, observedAt, o.DurationUs, o.SampleReason, string(o.RawOTLP)); err != nil {
					return err
				}
			}
			return nil
		})
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
	var (
		o   repo.Observation
		raw string
	)
	if err := s.Scan(&o.TransactionID, &o.TraceID, &o.ObservedAt, &o.DurationUs, &o.SampleReason, &raw); err != nil {
		return repo.Observation{}, err
	}
	o.RawOTLP = []byte(raw)
	return o, nil
}

// latestObservation returns the single newest trace matching where, including
// raw_otlp. ORDER BY observed_at DESC + LIMIT 1 BY trace_id collapses
// ReplacingMergeTree duplicates to the newest copy without FINAL.
func (r *Repo) latestObservation(ctx context.Context, what, where string, args ...any) (repo.Observation, bool, error) {
	return queryRow(ctx, r.conn, what,
		`SELECT `+observationCols+` FROM trace_observations WHERE `+where+`
		 ORDER BY observed_at DESC LIMIT 1 BY trace_id LIMIT 1`,
		scanObservation, args...)
}

// ListObservationsByTransaction returns the newest examples for one
// (transaction, sample_reason), newest first. Metadata only - RawOTLP is
// empty; fetch the blob with GetObservationByTrace when needed (compare /
// accept / view-trace). This keeps list scans off the large OTLP column.
func (r *Repo) ListObservationsByTransaction(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	if limit <= 0 {
		limit = 50
	}
	filter := "transaction_id = ?"
	filterArgs := []any{transactionID}
	if sampleReason != "" {
		filter += " AND sample_reason = ?"
		filterArgs = append(filterArgs, sampleReason)
	}

	// Two-stage ranking on metadata only so we never sort/materialize blobs
	// just to pick the newest N trace_ids.
	q := strings.Builder{}
	q.WriteString(`SELECT ` + observationMetaCols + ` FROM trace_observations WHERE ` + filter)
	q.WriteString(` AND trace_id IN (
		SELECT trace_id FROM trace_observations WHERE ` + filter + `
		ORDER BY observed_at DESC LIMIT 1 BY trace_id LIMIT ?)`)
	q.WriteString(` ORDER BY observed_at DESC LIMIT 1 BY trace_id, sample_reason LIMIT ?`)

	args := make([]any, 0, len(filterArgs)*2+2)
	args = append(args, filterArgs...)
	args = append(args, filterArgs...)
	args = append(args, uint64(limit), uint64(limit))

	return queryRows(ctx, r.conn, "list observations", q.String(), scanObservationMeta, args...)
}

// GetObservationByTrace returns the stored trace (raw OTLP) for one
// (transaction, trace), ok=false when none was retained. Backs compare /
// accept / view-trace paths that need the blob.
func (r *Repo) GetObservationByTrace(ctx context.Context, transactionID int64, traceID string) (repo.Observation, bool, error) {
	return r.latestObservation(ctx, "get observation by trace",
		"transaction_id = ? AND trace_id = ?", transactionID, traceID)
}

// GetLatestObservationByReason returns the newest stored observation for a
// sample_reason across all transactions, ok=false when none was retained. Backs
// the guardrail evidence view.
func (r *Repo) GetLatestObservationByReason(ctx context.Context, sampleReason string) (repo.Observation, bool, error) {
	return r.latestObservation(ctx, "get observation by reason", "sample_reason = ?", sampleReason)
}

// ListObservationsWithRaw returns the newest stored traces for one
// (transaction, sample_reason) including raw OTLP, newest first. Cold path only
// (the recommendation miner); list/UI paths use the meta-only variant.
func (r *Repo) ListObservationsWithRaw(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	if limit <= 0 {
		limit = 50
	}
	return queryRows(ctx, r.conn, "list observations with raw",
		`SELECT `+observationCols+` FROM trace_observations
		 WHERE transaction_id = ? AND sample_reason = ?
		 ORDER BY observed_at DESC LIMIT 1 BY trace_id LIMIT ?`,
		scanObservation, transactionID, sampleReason, uint64(limit))
}

// CountObservationsByReason returns how many distinct traces are stored for
// one transaction, per sample_reason.
func (r *Repo) CountObservationsByReason(ctx context.Context, transactionID int64) (map[string]int, error) {
	rows, err := r.conn.Query(ctx,
		`SELECT sample_reason, uniqExact(trace_id) FROM trace_observations WHERE transaction_id = ? GROUP BY sample_reason`,
		transactionID)
	if err != nil {
		return nil, fmt.Errorf("count observations by reason: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			reason   string
			distinct uint64
		)
		if err := rows.Scan(&reason, &distinct); err != nil {
			return nil, fmt.Errorf("count observations by reason: %w", err)
		}
		out[reason] = int(distinct)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count observations by reason: %w", err)
	}
	return out, nil
}
