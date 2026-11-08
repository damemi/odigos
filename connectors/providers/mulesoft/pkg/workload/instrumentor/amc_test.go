package instrumentor

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	"github.com/odigos-io/odigos-enterprise/connectors/anypoint/fake"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// memInventory is an in-memory inventory over a fixed application list.
type memInventory struct {
	apps    map[string]*store.Application
	sources []basestore.SourceRecord
	deleted map[string]bool
	dest    domaindest.OTLPTarget
	hasDest bool
}

func (m *memInventory) ListApplications(_ context.Context, kinds ...string) ([]store.Application, error) {
	var out []store.Application
	for _, a := range m.apps {
		for _, k := range kinds {
			if a.Kind == k {
				out = append(out, *a)
			}
		}
	}
	return out, nil
}
func (m *memInventory) ListSources(context.Context) ([]basestore.SourceRecord, error) {
	return m.sources, nil
}
func (m *memInventory) DeletedSourceKeys(context.Context) (map[string]bool, error) {
	return m.deleted, nil
}
func (m *memInventory) OTLPExportTarget(context.Context) (domaindest.OTLPTarget, bool, error) {
	return m.dest, m.hasDest, nil
}
func (m *memInventory) SetInstrumented(_ context.Context, id, origin string) error {
	m.apps[id].Instrumented, m.apps[id].Origin = origin != "", origin
	return nil
}
func (m *memInventory) SetInstrumentationError(_ context.Context, id, msg string) error {
	m.apps[id].InstrumentationError = msg
	return nil
}
func (m *memInventory) ClearInstrumentationError(_ context.Context, id string) error {
	m.apps[id].InstrumentationError = ""
	return nil
}
func (m *memInventory) SetInstrumentationPhase(_ context.Context, id, phase string) error {
	m.apps[id].InstrumentationPhase = phase
	return nil
}
func (m *memInventory) ClearInstrumentationPhase(_ context.Context, id string) error {
	m.apps[id].InstrumentationPhase = ""
	return nil
}
func (m *memInventory) SetGatewaySnapshot(_ context.Context, id string, snap *store.GatewaySnapshot) error {
	m.apps[id].GatewaySnapshot = snap
	return nil
}

type harness struct {
	srv *fake.Server
	inv *memInventory
	rec *AMC
}

func newHarness(t *testing.T, st fake.State, maxRedeploys int) *harness {
	t.Helper()
	srv := fake.NewServer(st)
	t.Cleanup(srv.Close)
	client := anypoint.New(anypoint.Config{BaseURL: srv.URL, ClientID: fake.FixtureClientID, ClientSecret: fake.FixtureClientSecret, Backoff: time.Millisecond})
	inv := &memInventory{apps: map[string]*store.Application{}, dest: grpcDest, hasDest: true}
	for env, deps := range st.Deployments {
		for _, d := range deps {
			inv.apps[d.ID] = &store.Application{
				ExternalID: d.ID, Kind: store.KindCloudHub2, AppName: d.Name, EnvironmentID: env,
				EnvironmentName: map[string]string{fake.FixtureEnvSandbox: "Sandbox", fake.FixtureEnvProduction: "Production"}[env],
				TargetType:      anypoint.TargetSharedSpace,
			}
		}
	}
	settings := func() Settings { return Settings{SampleRatio: 1, MaxRedeploys: maxRedeploys} }
	return &harness{srv: srv, inv: inv, rec: NewAMC(store.KindCloudHub2, inv, client, fake.FixtureOrgID, settings)}
}

func (h *harness) enable(ids ...string) {
	h.inv.sources = nil
	for _, id := range ids {
		h.inv.sources = append(h.inv.sources, basestore.SourceRecord{
			Name: h.inv.apps[id].AppName, Kind: store.KindCloudHub2, Enabled: true, ExternalResourceID: id,
		})
	}
}

func (h *harness) props(t *testing.T, env, id string) map[string]string {
	t.Helper()
	d, ok := h.srv.Handler().Deployment(env, id)
	if !ok {
		t.Fatalf("deployment %s not found", id)
	}
	return d.Properties()
}

func TestReconcileInstrumentsOnceThenConverges(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	h.enable(fake.FixtureOrderSandbox)
	ctx := context.Background()

	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	props := h.props(t, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)
	if props[PropTracesEndpoint] != "https://collector.example.com:4317" || props[store.PropOdigosManaged] == "" {
		t.Fatalf("not instrumented: %v", props)
	}
	if props["inventory.url"] != "http://inventory-api" {
		t.Error("the application's own properties must survive instrumentation")
	}
	if !h.inv.apps[fake.FixtureOrderSandbox].Instrumented {
		t.Error("inventory not marked instrumented")
	}

	// A converged application must never be patched again: every patch is a redeploy.
	for i := 0; i < 3; i++ {
		if err := h.rec.Reconcile(ctx); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if n := len(h.srv.Handler().Patches()); n != 1 {
		t.Fatalf("patches = %d, want exactly 1", n)
	}
	if h.inv.apps[fake.FixtureOrderSandbox].InstrumentationPhase != "" {
		t.Error("phase should clear once the deployment is applied and running")
	}
}

func TestDisablingTheSourceRestoresTheOriginalProperties(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	ctx := context.Background()
	original := h.props(t, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)

	h.enable(fake.FixtureOrderSandbox)
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	h.enable() // source disabled
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.props(t, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox); !maps.Equal(got, original) {
		t.Fatalf("after rollback = %v\nwant %v", got, original)
	}
	if h.inv.apps[fake.FixtureOrderSandbox].Instrumented {
		t.Error("inventory still marked instrumented")
	}
}

func TestIneligibleRuntimeIsReportedAndNeverPatched(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	h.enable(fake.FixtureLegacyApp)

	err := h.rec.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected an error for Mule 4.10")
	}
	if msg := h.inv.apps[fake.FixtureLegacyApp].InstrumentationError; !strings.Contains(msg, "4.11") {
		t.Errorf("instrumentation error = %q, want it to name the required version", msg)
	}
	if n := len(h.srv.Handler().Patches()); n != 0 {
		t.Fatalf("patches = %d, want 0", n)
	}
}

func TestCustomerTelemetryConfigIsNeverOverwritten(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	h.enable(fake.FixtureConflictApp)

	err := h.rec.Reconcile(context.Background())
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want a ConflictError", err)
	}
	if n := len(h.srv.Handler().Patches()); n != 0 {
		t.Fatalf("patches = %d, want 0", n)
	}
}

func TestRedeployBudgetBoundsOnePass(t *testing.T) {
	st := fake.SampleState()
	h := newHarness(t, st, 1)
	h.enable(fake.FixtureOrderSandbox, fake.FixtureOrderProduction)
	ctx := context.Background()

	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(h.srv.Handler().Patches()); n != 1 {
		t.Fatalf("first pass patches = %d, want 1 (the budget)", n)
	}
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(h.srv.Handler().Patches()); n != 2 {
		t.Fatalf("second pass patches = %d, want 2 (the deferred one done next)", n)
	}
}

// TestDeferredRollbackIsNotAClean: deleting a connector disables every source and runs reconcile passes until
// one returns nil. A rollback deferred because the deployment is mid-update must keep the pass from returning
// nil, or the connector is released with that application still instrumented and nothing left to roll it back.
func TestDeferredRollbackIsNotAClean(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	h.enable(fake.FixtureOrderSandbox, fake.FixtureOrderProduction)
	ctx := context.Background()
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// Someone else's update is rolling out on one of them.
	dep, _ := h.srv.Handler().Deployment(fake.FixtureEnvProduction, fake.FixtureOrderProduction)
	dep.Status = "APPLYING"
	h.srv.Handler().SetDeployment(fake.FixtureEnvProduction, dep)

	h.enable() // what the drain does
	err := h.rec.Reconcile(ctx)
	if !errors.Is(err, errDeferred) {
		t.Fatalf("first drain pass = %v, want a deferred error: one rollback is still pending", err)
	}
	for _, a := range h.inv.apps {
		if a.InstrumentationError != "" {
			t.Errorf("%s: a deferral is not a workload failure, got error %q", a.AppName, a.InstrumentationError)
		}
	}
	h.srv.Handler().Settle()
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatalf("second drain pass = %v, want nil once both are rolled back", err)
	}
	for _, id := range []string{fake.FixtureOrderSandbox, fake.FixtureOrderProduction} {
		if h.inv.apps[id].Instrumented {
			t.Errorf("%s still instrumented after the drain", id)
		}
	}
}

// TestRollbackIgnoresBudgetAndStoppedApplications: the drain on connector delete has a deadline, and taking
// Odigos's properties off a stopped application is exactly the end state wanted, so neither the redeploy budget
// nor a stopped application holds a rollback back.
func TestRollbackIgnoresBudgetAndStoppedApplications(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 1)
	h.enable(fake.FixtureOrderSandbox, fake.FixtureOrderProduction)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := h.rec.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	dep, _ := h.srv.Handler().Deployment(fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)
	dep.Application.DesiredState = "STOPPED"
	h.srv.Handler().SetDeployment(fake.FixtureEnvSandbox, dep)

	h.enable()
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatalf("drain pass = %v, want both rolled back in one pass", err)
	}
	if _, ok := h.props(t, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)[store.PropOdigosManaged]; ok {
		t.Error("the stopped application was not rolled back")
	}
	if _, ok := h.props(t, fake.FixtureEnvProduction, fake.FixtureOrderProduction)[store.PropOdigosManaged]; ok {
		t.Error("the second rollback was held back by the redeploy budget")
	}
}

// TestStaleInstrumentedFlagStillRollsBack: discovery can overwrite the instrumented flag with a read taken just
// before the PATCH landed. An application whose source is disabled, or deleted, is checked live instead.
func TestStaleInstrumentedFlagStillRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(h *harness)
	}{
		{"source disabled", func(h *harness) { h.inv.sources[0].Enabled = false }},
		{"source deleted", func(h *harness) {
			h.inv.sources = nil
			h.inv.deleted = map[string]bool{basestore.SourceIdentityKey(store.KindCloudHub2, fake.FixtureOrderSandbox): true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fake.SampleState(), 10)
			h.enable(fake.FixtureOrderSandbox)
			ctx := context.Background()
			if err := h.rec.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			h.inv.apps[fake.FixtureOrderSandbox].Instrumented = false // the stale discovery write
			tc.after(h)
			if err := h.rec.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if _, ok := h.props(t, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)[store.PropOdigosManaged]; ok {
				t.Fatal("not rolled back")
			}
			// Verified clean: later passes do not read it again.
			patches := len(h.srv.Handler().Patches())
			if err := h.rec.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if n := len(h.srv.Handler().Patches()); n != patches {
				t.Errorf("patches = %d after a clean pass, want %d", n, patches)
			}
		})
	}
}

// TestCopiedMarkerIsRefused: an application promoted from an instrumented one carries that one's marker. It is
// reported, never patched, and never "rolled back" to the other application's prior state.
func TestCopiedMarkerIsRefused(t *testing.T) {
	st := fake.SampleState()
	dep := st.Deployments[fake.FixtureEnvSandbox][0]
	copied, err := Apply(dep.Properties(), DesiredProperties(Workload{ServiceName: "order-api", OrgID: fake.FixtureOrgID}, grpcDest, 1), "", "dep-elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	dep.Application.Configuration.PropertiesService.Properties = copied
	st.Deployments[fake.FixtureEnvSandbox][0] = dep
	h := newHarness(t, st, 10)
	h.enable(dep.ID)

	if err := h.rec.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "another deployment") {
		t.Fatalf("reconcile = %v, want the copied marker reported", err)
	}
	h.enable()
	h.inv.apps[dep.ID].Instrumented, h.inv.apps[dep.ID].Origin = true, store.OriginConnector
	if err := h.rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(h.srv.Handler().Patches()); n != 0 {
		t.Fatalf("patches = %d, want 0", n)
	}
	// Recorded as discovery records it: instrumented, and not the connector's. Recording it uninstrumented had
	// the next discovery set it back and the reconciler read the deployment again, every cycle.
	app := h.inv.apps[dep.ID]
	if !app.Instrumented || app.Origin != store.OriginExternal {
		t.Errorf("after refusing the rollback: instrumented=%v origin=%q, want true/%q", app.Instrumented, app.Origin, store.OriginExternal)
	}
	if got := store.MarkerOrigin(copied, nil, dep.ID); got != app.Origin {
		t.Errorf("discovery would record origin %q, the reconciler recorded %q", got, app.Origin)
	}
}

func TestInFlightDeploymentIsDeferredAndVerifiedLater(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	h.srv.Handler().SettleOnPatch = false
	h.enable(fake.FixtureOrderSandbox)
	ctx := context.Background()

	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	// Rolling: converged properties, deployment not yet applied.
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.inv.apps[fake.FixtureOrderSandbox].InstrumentationPhase; got != baseapi.PhaseVerifying {
		t.Errorf("phase while rolling = %q, want %q", got, baseapi.PhaseVerifying)
	}
	h.srv.Handler().Settle()
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.inv.apps[fake.FixtureOrderSandbox].InstrumentationPhase; got != "" {
		t.Errorf("phase after settling = %q, want cleared", got)
	}
	if n := len(h.srv.Handler().Patches()); n != 1 {
		t.Fatalf("patches = %d, want 1", n)
	}
}

func TestCustomerInstrumentedApplicationIsNeverRolledBack(t *testing.T) {
	st := fake.SampleState()
	dep := st.Deployments[fake.FixtureEnvSandbox][0]
	dep.Application.Configuration.PropertiesService.Properties[store.PropOdigosIaC] = `{"v":1,"lang":"mule"}`
	st.Deployments[fake.FixtureEnvSandbox][0] = dep
	h := newHarness(t, st, 10)
	h.inv.apps[dep.ID].Instrumented, h.inv.apps[dep.ID].Origin = true, store.OriginExternal

	if err := h.rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(h.srv.Handler().Patches()); n != 0 {
		t.Fatalf("patches = %d, want 0", n)
	}
	if a := h.inv.apps[dep.ID]; !a.Instrumented || a.Origin != store.OriginExternal {
		t.Errorf("inventory = %+v, want it still reported as customer-instrumented", a)
	}
}

func TestNoDestinationSkipsInstrumentButStillRollsBack(t *testing.T) {
	h := newHarness(t, fake.SampleState(), 10)
	h.enable(fake.FixtureOrderSandbox)
	ctx := context.Background()
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	h.inv.hasDest = false
	h.enable(fake.FixtureOrderProduction)
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.props(t, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)[store.PropOdigosManaged]; ok {
		t.Error("the disabled source's application should have been rolled back without a destination")
	}
	if _, ok := h.props(t, fake.FixtureEnvProduction, fake.FixtureOrderProduction)[store.PropOdigosManaged]; ok {
		t.Error("nothing may be instrumented without a destination")
	}
}

// TestAPIManagerPairedApplicationKeepsItsPairing: a Mule Gateway application — one paired with an API Manager
// instance through autodiscovery — carries api.id and the anypoint.platform client credentials, the secret
// usually as a secure property. Odigos instruments it like any other application; it must not report those as
// a conflict, must never send the secure secret (its value is write-only and a resend would overwrite it with a
// mask), and a rollback must leave the pairing exactly as it was, or the application loses its policies.
func TestAPIManagerPairedApplicationKeepsItsPairing(t *testing.T) {
	st := fake.SampleState()
	dep := st.Deployments[fake.FixtureEnvSandbox][0]
	dep.Application.Configuration = &anypoint.ApplicationConfiguration{PropertiesService: &anypoint.PropertiesService{
		ApplicationName: dep.Name,
		Properties: map[string]string{
			"inventory.url":               "http://inventory-api",
			"api.id":                      "19283746",
			"anypoint.platform.client_id": "gateway-client",
		},
		SecureProperties: map[string]string{"anypoint.platform.client_secret": "****"},
	}}
	st.Deployments[fake.FixtureEnvSandbox][0] = dep
	h := newHarness(t, st, 10)
	ctx := context.Background()
	original := h.props(t, fake.FixtureEnvSandbox, dep.ID)

	h.enable(dep.ID)
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatalf("instrumenting a paired application: %v", err)
	}
	got := h.props(t, fake.FixtureEnvSandbox, dep.ID)
	for k, v := range original {
		if got[k] != v {
			t.Errorf("instrumenting changed %s: %q, want %q", k, got[k], v)
		}
	}
	if got[PropTracesEnabled] != "true" {
		t.Errorf("the paired application was not instrumented: %v", got)
	}

	h.enable() // source disabled
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.props(t, fake.FixtureEnvSandbox, dep.ID); !maps.Equal(got, original) {
		t.Fatalf("after rollback = %v\nwant %v", got, original)
	}

	for _, p := range h.srv.Handler().Patches() {
		if _, sent := p.Properties["anypoint.platform.client_secret"]; sent {
			t.Error("a patch carried the secure client secret; its value cannot be read back, so resending it destroys it")
		}
	}
	after, _ := h.srv.Handler().Deployment(fake.FixtureEnvSandbox, dep.ID)
	if keys := after.SecurePropertyKeys(); len(keys) != 1 || keys[0] != "anypoint.platform.client_secret" {
		t.Errorf("secure properties after instrument and rollback = %v, want the client secret kept", keys)
	}
}
