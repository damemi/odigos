// Package clickhouse is the ClickHouse-backed implementation of
// repo.Repository. It replaces the former Postgres backend.
//
// The design keeps the database off the per-trace hot path (the engine's
// in-memory store, caches, and write-behind flusher sit above the
// repo.Repository seam) and leans on ClickHouse's background merges to do the
// cross-replica baseline merge instead of locking and rewriting rows:
//
//   - Set baselines (D1/D2/D5/D6) are stored as one row per member in a
//     ReplacingMergeTree, so the union across flushes and replicas is free and
//     duplicate inserts collapse (idempotent, no dedup key needed).
//   - Additive baselines (the D7 histogram) are stored as (bucket, count) rows
//     in a SummingMergeTree; each flush appends a delta and ClickHouse sums.
//   - Per-class scalars live in an AggregatingMergeTree (observation_count
//     summed, promoted OR'd, learning_started_at anchored to the earliest).
//
// The engine owns the per-class JSON shapes and supplies a repo.BaselineCodec
// that this package uses to split a class value into member/counter rows on
// write and rebuild it on read; the backend never interprets the bytes itself.
//
// Methods are grouped by concern across sibling files; this file holds the type,
// its lifecycle, and the shared write/query helpers.
package clickhouse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Repo is the concrete Repository backed by a single ClickHouse connection.
// Construct with Open; the zero value is not usable.
type Repo struct {
	conn  driver.Conn
	codec repo.BaselineCodec
}

// Open dials ClickHouse at dsn (clickhouse://user:pass@host:9000/db) and returns
// a Repo. It does NOT require the server to be reachable yet - the connection is
// lazy - so the service can construct the repo, serve health checks, and wait
// for the DB. The codec (from engine.NewBaselineCodec) decomposes/rebuilds each
// class's baseline value.
func Open(dsn string, codec repo.BaselineCodec) (*Repo, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	return &Repo{conn: conn, codec: codec}, nil
}

// Ping verifies the DB is reachable. Used by /readyz.
func (r *Repo) Ping(ctx context.Context) error {
	if r == nil || r.conn == nil {
		return fmt.Errorf("clickhouse: nil connection")
	}
	return r.conn.Ping(ctx)
}

// Close releases the connection. Idempotent.
func (r *Repo) Close() error {
	if r == nil || r.conn == nil {
		return nil
	}
	return r.conn.Close()
}

// ---------- shared helpers ----------

// rowScanner is the Scan surface common to a single-row driver.Row and an
// iterated driver.Rows, so one scan func per type serves both read styles.
type rowScanner interface{ Scan(dest ...any) error }

// queryRows runs query and maps each row with scan into a slice. what labels
// wrapped errors.
func queryRows[T any](ctx context.Context, conn driver.Conn, what, query string, scan func(rowScanner) (T, error), args ...any) ([]T, error) {
	rows, err := conn.Query(ctx, query, args...)
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
	return out, rows.Err()
}

// queryRow runs a single-row query; a missing row is (zero, false, nil).
func queryRow[T any](ctx context.Context, conn driver.Conn, what, query string, scan func(rowScanner) (T, error), args ...any) (T, bool, error) {
	v, err := scan(conn.QueryRow(ctx, query, args...))
	if err != nil {
		var zero T
		if strings.Contains(err.Error(), "no rows") {
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

// insert prepares a single-row batch, appends via fn, and sends it. All writes
// go through the batch API, even single rows.
func (r *Repo) insert(ctx context.Context, query string, fn func(driver.Batch) error) error {
	batch, err := r.conn.PrepareBatch(ctx, query)
	if err != nil {
		return err
	}
	if err := fn(batch); err != nil {
		_ = batch.Abort()
		return err
	}
	return batch.Send()
}

func abortAll(batches ...driver.Batch) {
	for _, b := range batches {
		_ = b.Abort()
	}
}

// intInClause renders a safe SQL IN list from int64 ids (no injection risk).
func intInClause(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// Compile-time check that Repo satisfies the repository seam.
var _ repo.Repository = (*Repo)(nil)
