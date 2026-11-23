package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
)

func openClient(t *testing.T, dsn string) *store.Client {
	t.Helper()
	c, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSchemaReady_AfterMigrations(t *testing.T) {
	c := openClient(t, pgtest.NewDatabase(t))
	if err := c.SchemaReady(context.Background()); err != nil {
		t.Fatalf("schema ready after migrations: %v", err)
	}
}

func TestSchemaReady_NamesMissingObject(t *testing.T) {
	for _, tc := range []struct {
		drop, missing string
	}{
		{"DROP TABLE interrogation_requested_probes", "interrogation_requested_probes"},
		{"DROP SCHEMA adk CASCADE", "adk.sessions"},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			dsn := pgtest.NewDatabase(t)
			if _, err := pgtest.Connect(t, dsn).Exec(context.Background(), tc.drop); err != nil {
				t.Fatal(err)
			}
			err := openClient(t, dsn).SchemaReady(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("SchemaReady = %v, want an error naming %s", err, tc.missing)
			}
		})
	}
}
