package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	cpg "github.com/odigos-io/odigos/connectors/postgres"
)

// TestDrainRequestsKeepTheirReason runs the drain SQL against a real, migrated Postgres. Opt in with
// BASE_STORE_TEST_DSN; the statements under test are conditional upserts whose behavior only Postgres can
// confirm.
//
// The reason decides what a drain does, so a request is never relabeled by the connector, and a clear only
// removes the request it was meant for. Either mistake turns a connector deletion into a manual cleanup that
// never records completion, or a manual cleanup into a deletion that stops the connector.
func TestDrainRequestsKeepTheirReason(t *testing.T) {
	dsn := os.Getenv("BASE_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("set BASE_STORE_TEST_DSN to a migrated Postgres to run")
	}
	ctx := context.Background()
	name := fmt.Sprintf("drain-test-%d", time.Now().UnixNano())
	s, err := Open(ctx, dsn, name, cpg.SchemaAWS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM connector_platform.runtime_heartbeat WHERE connector_name = $1`, name)
	}()
	if err := s.Register(ctx, "runtime-1", "pod-1", "dev", cpg.HeartbeatStatusConnected, nil); err != nil {
		t.Fatal(err)
	}
	reason := func() DrainState {
		t.Helper()
		d, err := s.DrainRequested(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// The controller's write, as connector-runtime makes it: an unconditional upsert.
	controllerDeletes := func() {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `
			UPDATE connector_platform.runtime_heartbeat
			SET drain_requested_at = now(), drain_reason = $2
			WHERE connector_name = $1`, name, cpg.DrainReasonConnectorDeleted); err != nil {
			t.Fatal(err)
		}
	}

	// A manual cleanup is recorded, and asking again is not an error.
	for range 2 {
		if err := s.RequestDrain(ctx, cpg.DrainReasonManualCleanup); err != nil {
			t.Fatalf("request manual cleanup: %v", err)
		}
	}
	if d := reason(); !d.Requested || d.Reason != cpg.DrainReasonManualCleanup {
		t.Fatalf("drain = %+v, want a pending manual cleanup", d)
	}

	// The controller replaces it with a deletion. Clearing the manual cleanup then must not clear that.
	controllerDeletes()
	if err := s.ClearDrain(ctx, cpg.DrainReasonManualCleanup); err != nil {
		t.Fatal(err)
	}
	if d := reason(); !d.Requested || d.Reason != cpg.DrainReasonConnectorDeleted {
		t.Fatalf("drain = %+v after clearing a manual cleanup, want the deletion kept", d)
	}

	// And a manual cleanup asked for during the deletion is refused rather than relabeling it.
	if err := s.RequestDrain(ctx, cpg.DrainReasonManualCleanup); !errors.Is(err, ErrOtherDrainPending) {
		t.Fatalf("manual cleanup during a deletion: err = %v, want ErrOtherDrainPending", err)
	}
	if d := reason(); d.Reason != cpg.DrainReasonConnectorDeleted {
		t.Fatalf("drain reason = %q, want the deletion kept", d.Reason)
	}

	// A matching clear does clear.
	if err := s.ClearDrain(ctx, cpg.DrainReasonConnectorDeleted); err != nil {
		t.Fatal(err)
	}
	if d := reason(); d.Requested {
		t.Fatalf("drain = %+v after a matching clear, want none", d)
	}

	// The database clock reads, and is close to this one (the test and the database share a host).
	now, err := s.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(now); d > time.Minute || d < -time.Minute {
		t.Fatalf("database clock is %v from the test's", d)
	}
}
