// Package postgres is the Postgres-backed implementation of repo.Repository.
//
// The database stays off the per-trace hot path: the engine's in-memory store,
// caches and write-behind flusher sit above the repo.Repository seam and hand
// this package batched writes. Writes that ClickHouse-style stores would merge
// in the background are merged here at write time with upserts:
//
//   - Set baselines (D1/D2/D5/D6) are one row per member; re-inserting a member
//     is a no-op.
//   - Additive baselines (the D7 histogram) and finding/activity counters add
//     the incoming delta to the stored row.
//   - Per-class scalars keep sum/min/max semantics in the upsert itself.
//
// The engine owns the per-class JSON shapes and supplies a repo.BaselineCodec
// that this package uses to split a class value into member/counter rows on
// write and rebuild it on read; the backend never interprets the bytes itself.
//
// Methods are grouped by concern across sibling files; this file holds the type,
// its lifecycle, and the shared write/query helpers.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Repo is the concrete Repository backed by a pgx connection pool.
// Construct with Open; the zero value is not usable.
type Repo struct {
	pool  *pgxpool.Pool
	codec repo.BaselineCodec
	// volumeBytes is the bundled data volume size, used by the storage page to
	// report disk usage. Zero when unknown (external Postgres).
	volumeBytes uint64
}

// Option configures optional Repo behavior.
type Option func(*Repo)

// WithVolumeBytes tells the storage page the size of the volume backing the
// database, so it can report how full it is.
func WithVolumeBytes(n uint64) Option {
	return func(r *Repo) { r.volumeBytes = n }
}

// Open builds a pool for dsn (postgres://user:pass@host:5432/db) and returns a
// Repo. It does NOT require the server to be reachable yet - connections are
// opened lazily - so the service can construct the repo, serve health checks,
// and wait for the DB.
func Open(dsn string, codec repo.BaselineCodec, opts ...Option) (*Repo, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	r := &Repo{pool: pool, codec: codec}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Ping verifies the DB is reachable. Used by /readyz.
func (r *Repo) Ping(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("postgres: nil pool")
	}
	return r.pool.Ping(ctx)
}

// Close releases the pool. Idempotent.
func (r *Repo) Close() error {
	if r == nil || r.pool == nil {
		return nil
	}
	r.pool.Close()
	return nil
}

// ---------- shared helpers ----------

// querier is the surface shared by the pool and a transaction, so a write can
// run standalone or as one step of a larger transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// rowScanner is the Scan surface common to pgx.Row and pgx.Rows, so one scan
// func per type serves both read styles.
type rowScanner interface{ Scan(dest ...any) error }

// queryRows runs query and maps each row with scan into a slice. what labels
// wrapped errors.
func queryRows[T any](ctx context.Context, q querier, what, query string, scan func(rowScanner) (T, error), args ...any) ([]T, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// queryRow runs a single-row query; a missing row is (zero, false, nil).
func queryRow[T any](ctx context.Context, q querier, what, query string, scan func(rowScanner) (T, error), args ...any) (T, bool, error) {
	v, err := scan(q.QueryRow(ctx, query, args...))
	if err != nil {
		var zero T
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, false, nil
		}
		return zero, false, fmt.Errorf("%s: %w", what, err)
	}
	return v, true, nil
}

func scanString(s rowScanner) (string, error) {
	var v string
	err := s.Scan(&v)
	return v, err
}

// maxTxAttempts bounds retries of a transaction that lost a deadlock or
// serialization race to another replica.
const maxTxAttempts = 3

// inTx runs fn in a transaction, retrying when Postgres aborts it as a
// deadlock victim or on a serialization failure. fn must be safe to re-run.
func (r *Repo) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err = pgx.BeginFunc(ctx, r.pool, fn)
		if err == nil || !retryableTxError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt) * 20 * time.Millisecond):
		}
	}
	return err
}

func retryableTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	// 40P01 deadlock_detected, 40001 serialization_failure.
	return pgErr.Code == "40P01" || pgErr.Code == "40001"
}

// execBatch queues one statement per row and sends them in a single round
// trip. Callers sort rows by key so concurrent replicas lock rows in the same
// order.
func execBatch(ctx context.Context, q querier, sql string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, args := range rows {
		b.Queue(sql, args...)
	}
	return q.SendBatch(ctx, b).Close()
}

// args collects positional parameters for dynamically built queries; add
// returns the placeholder for the value it appends.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// epoch is the "unset timestamp" sentinel every NOT NULL timestamp column
// defaults to, so GREATEST/LEAST merges never see NULL.
var epoch = time.Unix(0, 0).UTC()

// orEpoch maps a zero time to the epoch sentinel.
func orEpoch(t time.Time) time.Time {
	if t.IsZero() {
		return epoch
	}
	return t
}

// normalizeEpoch maps the epoch sentinel back to Go's zero time, so callers can
// test it with IsZero uniformly.
func normalizeEpoch(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t
}

// Compile-time check that Repo satisfies the repository seam.
var _ repo.Repository = (*Repo)(nil)
