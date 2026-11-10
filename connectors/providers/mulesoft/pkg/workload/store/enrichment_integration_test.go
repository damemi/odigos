package store

import (
	"context"
	"os"
	"testing"
	"time"

	basestore "github.com/odigos-io/odigos/connectors/base/store"
)

// TestEnrichmentAgainstPostgres pins the two metadata contracts the new columns rest on: an API pairing is
// kept when a discovery refresh does not mention it (API Manager unreadable) and cleared only by an explicit
// none, and a gateway snapshot of "no tracing" is told apart from "no snapshot".
func TestEnrichmentAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("MULESOFT_STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("MULESOFT_STORE_TEST_DSN not set")
	}
	ctx := context.Background()
	b, err := basestore.Open(ctx, dsn, "it-enrich-"+time.Now().Format("150405.000"), "connector_mulesoft")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := New(b)

	read := func(id string) Application {
		t.Helper()
		a, ok, err := s.ApplicationByID(ctx, id)
		if err != nil || !ok {
			t.Fatalf("read %s: found=%v err=%v", id, ok, err)
		}
		return a
	}

	app := Application{ExternalID: "dep-api", Kind: KindCloudHub2, AppName: "order-api", EnvironmentID: "env-1", EnvironmentName: "Production",
		Metadata: map[string]any{MetaAPI: &APIRef{ID: "18977143", AssetID: "order-api", Version: "v1"}}}
	if err := s.UpsertApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got := read("dep-api").API; got == nil || got.ID != "18977143" || got.Version != "v1" {
		t.Fatalf("pairing = %+v", got)
	}
	app.Metadata = map[string]any{} // API Manager unreadable this cycle
	if err := s.UpsertApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got := read("dep-api").API; got == nil || got.ID != "18977143" {
		t.Errorf("pairing after a refresh that did not read API Manager = %+v, want it kept", got)
	}
	app.Metadata = map[string]any{MetaAPI: (*APIRef)(nil)}
	if err := s.UpsertApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	if got := read("dep-api").API; got != nil {
		t.Errorf("pairing after an explicit none = %+v, want it cleared", got)
	}

	gw := Application{ExternalID: "gw-1", Kind: KindOmniGateway, AppName: "odigos-gateway-1", EnvironmentID: "env-1", EnvironmentName: "Sandbox", TargetType: GatewayManaged}
	if err := s.UpsertApplication(ctx, gw); err != nil {
		t.Fatal(err)
	}
	if got := read("gw-1").GatewaySnapshot; got != nil {
		t.Fatalf("snapshot before any = %+v", got)
	}
	if err := s.SetGatewaySnapshot(ctx, "gw-1", &GatewaySnapshot{Tracing: nil}); err != nil {
		t.Fatal(err)
	}
	if got := read("gw-1").GatewaySnapshot; got == nil || got.Tracing != nil {
		t.Errorf("snapshot of a gateway with no tracing = %+v, want a snapshot recording none", got)
	}
	if err := s.SetGatewaySnapshot(ctx, "gw-1", &GatewaySnapshot{Tracing: map[string]any{"enabled": false, "sampling": 25.0}}); err != nil {
		t.Fatal(err)
	}
	if got := read("gw-1").GatewaySnapshot; got == nil || got.Tracing["sampling"] != 25.0 {
		t.Errorf("snapshot = %+v", got)
	}
	if err := s.SetGatewaySnapshot(ctx, "gw-1", nil); err != nil {
		t.Fatal(err)
	}
	if got := read("gw-1").GatewaySnapshot; got != nil {
		t.Errorf("snapshot after clearing = %+v", got)
	}
	if ws, err := s.ListWorkloads(ctx); err != nil {
		t.Fatal(err)
	} else {
		for _, w := range ws {
			if w.Kind == KindOmniGateway && (w.RuntimeLanguage != "" || w.InstrumentationType != KindOmniGateway) {
				t.Errorf("gateway workload = %+v, want no language and its own instrumentation type", w)
			}
		}
	}
}
