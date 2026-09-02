package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// InsertObservation stores one trace as a UI/debug example. The caller (the
// engine's scoring and guardrail paths) picks the sample_reason; the repo does
// not sample.
func (r *Repo) InsertObservation(ctx context.Context, obs repo.Observation) error {
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now()
	}
	err := r.insert(ctx,
		"INSERT INTO trace_observations (transaction_id, trace_id, observed_at, duration_ms, sample_reason, raw_otlp)",
		func(b driver.Batch) error {
			return b.Append(obs.TransactionID, obs.TraceID, obs.ObservedAt, obs.DurationMs, obs.SampleReason, string(obs.RawOTLP))
		})
	if err != nil {
		return fmt.Errorf("insert observation: %w", err)
	}
	return nil
}

// Meta-only columns for list paths. raw_otlp is intentionally omitted — blobs
// are loaded via GetObservationByTrace / GetLatestObservationByReason.
const observationMetaCols = `transaction_id, trace_id, observed_at, duration_ms, sample_reason`

const observationCols = observationMetaCols + `, raw_otlp`

func scanObservationMeta(s rowScanner) (repo.Observation, error) {
	var o repo.Observation
	if err := s.Scan(&o.TransactionID, &o.TraceID, &o.ObservedAt, &o.DurationMs, &o.SampleReason); err != nil {
		return repo.Observation{}, err
	}
	return o, nil
}

func scanObservation(s rowScanner) (repo.Observation, error) {
	var (
		o   repo.Observation
		raw string
	)
	if err := s.Scan(&o.TransactionID, &o.TraceID, &o.ObservedAt, &o.DurationMs, &o.SampleReason, &raw); err != nil {
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
// (transaction, sample_reason), newest first. Metadata only — RawOTLP is
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

// CountObservations returns how many distinct traces are stored for one
// (transaction, sample_reason).
func (r *Repo) CountObservations(ctx context.Context, transactionID int64, sampleReason string) (int, error) {
	var distinct uint64
	if err := r.conn.QueryRow(ctx,
		`SELECT uniqExact(trace_id) FROM trace_observations WHERE transaction_id = ? AND sample_reason = ?`,
		transactionID, sampleReason).Scan(&distinct); err != nil {
		return 0, fmt.Errorf("count observations: %w", err)
	}
	return int(distinct), nil
}
