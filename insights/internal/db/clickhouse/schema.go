package clickhouse

import (
	"context"
	"fmt"
)

// SchemaReady reports whether the schema migrations have been applied. The app
// no longer creates the schema itself — that's done once per rollout by the
// migration Job (see migrate.Apply) — so replicas use this to stay NOT-ready
// until the Job has finished, instead of serving against a missing/half-built
// schema.
//
// It checks that learning_policies is present AND seeded: that row only exists
// after the seed migration, so it doubles as "all migrations applied".
func (r *Repo) SchemaReady(ctx context.Context) error {
	var n uint64
	if err := r.conn.QueryRow(ctx, "SELECT count() FROM learning_policies").Scan(&n); err != nil {
		return fmt.Errorf("schema not ready: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("schema not ready: learning_policies not seeded yet")
	}
	return nil
}
