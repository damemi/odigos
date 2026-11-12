package store

import (
	"context"
	"os"
	"testing"
	"time"

	basestore "github.com/odigos-io/odigos/connectors/base/store"
)

// TestStoreAgainstPostgres exercises the inventory SQL against a database migrated with
// connector-postgres/migrations. It is opt-in: set MULESOFT_STORE_TEST_DSN to a scratch database's URL.
func TestStoreAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MULESOFT_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("MULESOFT_STORE_TEST_DSN not set")
	}
	ctx := context.Background()
	b, err := basestore.Open(ctx, dsn, "it-"+time.Now().Format("150405.000"), "connector_mulesoft")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()
	s := New(b)

	// The base tables the framework reads must have the shape it expects in this schema.
	if _, err := s.ListSources(ctx); err != nil {
		t.Fatalf("base ListSources on connector_mulesoft: %v", err)
	}
	if _, _, err := s.OTLPExportTarget(ctx); err != nil {
		t.Fatalf("base OTLPExportTarget on connector_mulesoft: %v", err)
	}

	app := Application{
		ExternalID: "dep-1", Kind: KindCloudHub2, AppName: "order-api", EnvironmentID: "env-1",
		EnvironmentName: "Sandbox", TargetID: "cloudhub-us-east-2", TargetType: "shared-space",
		TargetRegion: "us-east-2", RuntimeVersion: "4.12.0:2e-java17", Status: "APPLIED",
		Metadata: map[string]any{"replicas": 1},
	}
	prod := app
	prod.ExternalID, prod.EnvironmentID, prod.EnvironmentName = "dep-2", "env-2", "Production"
	hybrid := Application{ExternalID: "env-2/41", Kind: KindHybrid, AppName: "ledger-api", EnvironmentID: "env-2", EnvironmentName: "Production"}
	for _, a := range []Application{app, prod, hybrid} {
		if err := s.UpsertApplication(ctx, a); err != nil {
			t.Fatalf("upsert %s: %v", a.ExternalID, err)
		}
	}

	if err := s.SetInstrumentationPhase(ctx, "dep-1", "Instrumenting"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInstrumented(ctx, "dep-1", OriginConnector); err != nil {
		t.Fatal(err)
	}
	// A discovery refresh must keep the instrumentor's phase.
	app.Origin = OriginConnector
	if err := s.UpsertApplication(ctx, app); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListApplications(ctx, KindCloudHub2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("cloudhub2 apps = %d, want 2", len(got))
	}
	var first Application
	for _, a := range got {
		if a.ExternalID == "dep-1" {
			first = a
		}
	}
	if !first.Instrumented || first.Origin != OriginConnector || first.InstrumentationPhase != "Instrumenting" {
		t.Errorf("dep-1 = %+v, want instrumented by the connector, phase kept", first)
	}

	since := time.Now().UTC().Truncate(time.Second)
	if err := s.SetObservation(ctx, "dep-1", Observation{
		Conformance: "Partial", Missing: []string{"collectorExport"}, Since: since, Settling: true,
	}); err != nil {
		t.Fatalf("set observation: %v", err)
	}
	byID, found, err := s.ApplicationByID(ctx, "dep-1")
	if err != nil || !found {
		t.Fatalf("by id = %v %v", found, err)
	}
	if o := byID.Observation; o.Conformance != "Partial" || len(o.Missing) != 1 || !o.Since.Equal(since) || !o.Settling {
		t.Errorf("observation round trip = %+v", o)
	}
	if _, found, err := s.ApplicationByID(ctx, "nope"); err != nil || found {
		t.Errorf("missing id = %v %v", found, err)
	}

	ws, err := s.ListWorkloads(ctx)
	if err != nil || len(ws) != 3 {
		t.Fatalf("workloads = %d, %v; want 3", len(ws), err)
	}

	id, env, found, err := s.LookupDiscoveredWorkload(ctx, "order-api", KindCloudHub2, "Production")
	if err != nil || !found || id != "dep-2" || env != "Production" {
		t.Errorf("lookup = %q %q %v %v", id, env, found, err)
	}

	// The same application name in two environments is ambiguous without one.
	if _, _, _, err := s.LookupDiscoveredWorkload(ctx, "order-api", KindCloudHub2, ""); err == nil {
		t.Error("an unqualified lookup of an application in two environments must be refused")
	}

	// The prune cutoff comes from the database clock, so a row upserted after reading it is never pruned by it,
	// whatever the skew between this process and Postgres.
	cutoff, err := s.Now(ctx)
	if err != nil {
		t.Fatalf("db now: %v", err)
	}
	if err := s.UpsertApplication(ctx, prod); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(ctx, KindCloudHub2, cutoff, PruneScope{Complete: []string{"env-1", "env-2"}}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.ApplicationByID(ctx, "dep-2"); !found {
		t.Fatal("a row upserted after the database-clock cutoff was pruned")
	}

	// Only environments the scan read in full are pruned.
	later := time.Now().Add(time.Hour)
	if err := s.Prune(ctx, KindCloudHub2, later, PruneScope{Complete: []string{"env-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.ApplicationByID(ctx, "dep-2"); !found {
		t.Fatal("a row in an environment the scan could not read was pruned")
	}
	if _, found, _ := s.ApplicationByID(ctx, "dep-1"); found {
		t.Fatal("a row the complete environment no longer has was kept")
	}
	// An environment missing from a complete environment list is gone with its rows.
	if err := s.Prune(ctx, KindCloudHub2, later, PruneScope{Listed: []string{"env-1"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListApplications(ctx, KindCloudHub2); len(got) != 0 {
		t.Errorf("after prune: %d cloudhub2 apps live, want 0", len(got))
	}
	if got, _ := s.ListApplications(ctx, KindHybrid); len(got) != 1 {
		t.Errorf("pruning one kind must not touch another, hybrid = %d", len(got))
	}
	if err := s.Prune(ctx, KindHybrid, later, PruneScope{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListApplications(ctx, KindHybrid); len(got) != 1 {
		t.Errorf("an empty scope must prune nothing, hybrid = %d", len(got))
	}
	if err := s.PurgeDeleted(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := s.SetOwnerOrganization(ctx, "org-1"); err != nil {
		t.Fatal(err)
	}
	if org, ok, err := s.OwnerOrganization(ctx); err != nil || !ok || org != "org-1" {
		t.Errorf("owner = %q %v %v", org, ok, err)
	}
	if err := s.UpdateDiscoveryState(ctx, "env-1", KindCloudHub2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeConnectorInventory(ctx); err != nil {
		t.Fatal(err)
	}
}
