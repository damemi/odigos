package postgres

import (
	"context"
	"fmt"
)

// SchemaReady reports whether the schema migrations have been applied. The app
// does not create the schema itself - the migration Job does (see
// migrate.Apply) - so replicas use this to stay NOT-ready until the Job has
// finished, instead of serving against a missing schema.
//
// It checks that learning_policies is present AND seeded: that row only exists
// after the seed, so it doubles as "all migrations applied".
func (r *Repo) SchemaReady(ctx context.Context) error {
	var n int64
	if err := r.pool.QueryRow(ctx, "SELECT count(*) FROM learning_policies").Scan(&n); err != nil {
		return fmt.Errorf("schema not ready: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("schema not ready: learning_policies not seeded yet")
	}
	return nil
}
