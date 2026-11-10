package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	basestore "github.com/odigos-io/odigos/connectors/base/store"
)

// An explicitly supplied disposable database must already have the connector migrations applied.
func TestLambdaSnapshotPostgres(t *testing.T) {
	dsn := os.Getenv("ODIGOS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set ODIGOS_TEST_POSTGRES_URL to a migrated disposable database")
	}
	ctx := context.Background()
	open := func(name string) *Store {
		base, err := basestore.Open(ctx, dsn, name, "connector_aws")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(base.Close)
		return New(base)
	}
	a := open("snapshot-test-a")
	b := open("snapshot-test-b")
	t.Cleanup(func() {
		_, _ = a.Pool().Exec(ctx, `DELETE FROM connector_aws.lambda_instrumentation_snapshots WHERE connector_name IN ('snapshot-test-a','snapshot-test-b')`)
	})
	payload := []byte(`{"v":1,"prevEnv":{"CONFIG":"original","ABSENT":null}}`)
	if err := a.SaveLambdaSnapshot(ctx, "function-a", "id", payload); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveLambdaSnapshot(ctx, "function-a", "id", []byte(`{"changed":true}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := a.LoadLambdaSnapshot(ctx, "function-a", "id")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		PrevEnv map[string]*string `json:"prevEnv"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.PrevEnv["CONFIG"] == nil || *document.PrevEnv["CONFIG"] != "original" || document.PrevEnv["ABSENT"] != nil {
		t.Fatal("immutable snapshot was replaced")
	}
	if _, err := a.LoadLambdaSnapshot(ctx, "function-b", "id"); err == nil {
		t.Fatal("cross-function read succeeded")
	}
	if _, err := b.LoadLambdaSnapshot(ctx, "function-a", "id"); err == nil {
		t.Fatal("cross-connector read succeeded")
	}
	restarted := open("snapshot-test-a")
	if _, err := restarted.LoadLambdaSnapshot(ctx, "function-a", "id"); err != nil {
		t.Fatal(err)
	}
}
