package instrumentor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	"github.com/odigos-io/odigos-enterprise/connectors/anypoint/fake"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

type gatewayHarness struct {
	srv *fake.Server
	inv *memInventory
	rec *AMC
}

func newGatewayHarness(t *testing.T, st fake.State) *gatewayHarness {
	t.Helper()
	srv := fake.NewServer(st)
	t.Cleanup(srv.Close)
	client := anypoint.New(anypoint.Config{BaseURL: srv.URL, ClientID: fake.FixtureClientID, ClientSecret: fake.FixtureClientSecret, Backoff: time.Millisecond})
	inv := &memInventory{apps: map[string]*store.Application{}, dest: grpcDest, hasDest: true}
	for env, gws := range st.Gateways {
		for _, raw := range gws {
			g := anypoint.NewGatewayDetail(raw).Summary()
			targetType := store.GatewaySelfManaged
			if g.Managed() {
				targetType = store.GatewayManaged
			}
			inv.apps[g.ID] = &store.Application{
				ExternalID: g.ID, Kind: store.KindOmniGateway, AppName: g.Name, EnvironmentID: env,
				EnvironmentName: "Sandbox", TargetType: targetType, Status: g.Status,
			}
		}
	}
	settings := func() Settings { return Settings{SampleRatio: 1, MaxRedeploys: 10} }
	return &gatewayHarness{srv: srv, inv: inv, rec: NewAMC(store.KindOmniGateway, inv, client, fake.FixtureOrgID, settings)}
}

func (h *gatewayHarness) enable(ids ...string) {
	h.inv.sources = nil
	for _, id := range ids {
		h.inv.sources = append(h.inv.sources, basestore.SourceRecord{
			Name: h.inv.apps[id].AppName, Kind: store.KindOmniGateway, Enabled: true, ExternalResourceID: id,
		})
	}
}

func (h *gatewayHarness) tracing(t *testing.T) map[string]any {
	t.Helper()
	raw, ok := h.srv.Handler().Gateway(fake.FixtureEnvSandbox, fake.FixtureManagedGateway)
	if !ok {
		t.Fatal("managed gateway missing")
	}
	return anypoint.NewGatewayDetail(raw).Tracing()
}

func TestManagedGatewayTracingTurnsOnAndConverges(t *testing.T) {
	h := newGatewayHarness(t, fake.SampleState())
	ctx := context.Background()
	h.enable(fake.FixtureManagedGateway)

	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	tr := h.tracing(t)
	p := gatewayProvider(tr)
	if tr["enabled"] != true || p.typ != "opentelemetry" || p.props["serviceType"] != "grpc" ||
		p.props["address"] != "https://collector.example.com:4317" || p.props["serviceName"] != "odigos-gateway-1" {
		t.Fatalf("tracing after instrument = %v", tr)
	}
	if !store.GatewayHasLabel(tr, store.GatewayManagedLabel) {
		t.Error("Odigos's label is missing, so nothing could prove the right to roll it back")
	}
	app := h.inv.apps[fake.FixtureManagedGateway]
	if !app.Instrumented || app.Origin != store.OriginConnector {
		t.Errorf("inventory = %+v, want instrumented by the connector", app)
	}
	if app.GatewaySnapshot == nil || app.GatewaySnapshot.Tracing != nil {
		t.Errorf("snapshot = %+v, want one recording that the gateway had no tracing", app.GatewaySnapshot)
	}
	// Converged: a second pass changes nothing, because every change rolls the gateway.
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(h.srv.Handler().GatewayUpdates()); n != 1 {
		t.Errorf("updates = %d, want 1", n)
	}
}

func TestDisablingTheGatewaySourceRestoresItsTracing(t *testing.T) {
	st := fake.SampleState()
	// The customer had tracing configured but off, with a label of their own and a sampling choice.
	st.Gateways[fake.FixtureEnvSandbox][0]["configuration"].(map[string]any)["tracing"] = map[string]any{
		"enabled": false, "sampling": 25.0,
		"labels": []any{map[string]any{"name": "team", "type": "literal", "defaultValue": "payments"}},
	}
	h := newGatewayHarness(t, st)
	ctx := context.Background()
	before := h.tracing(t)

	h.enable(fake.FixtureManagedGateway)
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	during := h.tracing(t)
	if during["sampling"] != 25.0 || !store.GatewayHasLabel(during, "team") {
		t.Errorf("instrumenting dropped the customer's sampling or label: %v", during)
	}
	h.enable() // source disabled
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if after := h.tracing(t); !reflect.DeepEqual(after, before) {
		t.Fatalf("after rollback = %v\nwant %v", after, before)
	}
	app := h.inv.apps[fake.FixtureManagedGateway]
	if app.Instrumented || app.GatewaySnapshot != nil {
		t.Errorf("inventory after rollback = %+v, want uninstrumented with no snapshot", app)
	}
}

func TestSelfManagedGatewayIsNotChanged(t *testing.T) {
	h := newGatewayHarness(t, fake.SampleState())
	h.enable(fake.FixtureSelfGateway)
	err := h.rec.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "self-managed") || !strings.Contains(err.Error(), "manual mode") {
		t.Fatalf("reconcile = %v, want the self-managed gateway explained", err)
	}
	if n := len(h.srv.Handler().GatewayUpdates()); n != 0 {
		t.Errorf("updates = %d, want 0", n)
	}
}

func TestGatewayTracingTheCustomerSetUpIsNeverReplaced(t *testing.T) {
	st := fake.SampleState()
	st.Gateways[fake.FixtureEnvSandbox][0]["configuration"].(map[string]any)["tracing"] = map[string]any{
		"enabled": true, "provider": map[string]any{"type": "anypoint"},
	}
	h := newGatewayHarness(t, st)
	h.enable(fake.FixtureManagedGateway)
	err := h.rec.Reconcile(context.Background())
	var conflict *GatewayConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("reconcile = %v, want a conflict", err)
	}
	if n := len(h.srv.Handler().GatewayUpdates()); n != 0 {
		t.Errorf("updates = %d, want 0", n)
	}
}

// Gateway Manager's JSON for a custom tracing provider is not published. If Anypoint accepts the update but
// drops the provider, the gateway would trace to Anypoint Monitoring with Odigos believing it traces to the
// destination; the write is undone and reported instead.
func TestAProviderAnypointDidNotKeepIsUndone(t *testing.T) {
	h := newGatewayHarness(t, fake.SampleState())
	h.srv.Handler().DropTracingProvider = true
	h.enable(fake.FixtureManagedGateway)
	err := h.rec.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not keep") {
		t.Fatalf("reconcile = %v, want the dropped provider reported", err)
	}
	if tr := h.tracing(t); tr != nil {
		t.Errorf("tracing after the undo = %v, want none, as before", tr)
	}
	if app := h.inv.apps[fake.FixtureManagedGateway]; app.Instrumented || app.GatewaySnapshot != nil {
		t.Errorf("after the undo: %+v, want not instrumented and no snapshot", app)
	}
}

func TestGatewayManualPlanValidatesAndResolves(t *testing.T) {
	pl, err := GatewayManualPlan(store.KindOmniGateway)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, e := range pl.Fragments[0].Env {
		names[e.Name] = e.Value
	}
	if names[GatewayTracingIaCLabelKey] != GatewayIaCLabelValue || names[GatewayProviderType] != "opentelemetry" {
		t.Errorf("plan = %v", names)
	}
	if got := GatewayManualValues(grpcDest, true); got[PlaceholderGatewayServiceType] != "grpc" || got[PlaceholderTracesEndpoint] != "https://collector.example.com:4317" {
		t.Errorf("values = %v", got)
	}
	if got := GatewayManualValues(grpcDest, false); len(got) != 0 {
		t.Errorf("values with no destination = %v, want none", got)
	}
}

// The verdict a manual-mode gateway gets from what the customer configured.
func TestJudgeGateway(t *testing.T) {
	applied := func(serviceType, address, name string) map[string]any {
		return map[string]any{
			"enabled": true,
			"labels":  []any{map[string]any{"name": store.GatewayIaCLabel, "type": "literal", "defaultValue": GatewayIaCLabelValue}},
			"provider": map[string]any{"type": "opentelemetry", "properties": map[string]any{
				"serviceType": serviceType, "address": address, "serviceName": name,
			}},
		}
	}
	cases := []struct {
		name    string
		tracing map[string]any
		want    string
		missing []string
	}{
		{"nothing applied", nil, "Absent", nil},
		{"as published", applied("grpc", "https://collector.example.com:4317", "gw"), "Conformant", nil},
		{"wrong endpoint", applied("grpc", "https://elsewhere:4317", "gw"), "Partial", []string{"collectorExport"}},
		{"wrong service name", applied("GRPC", "https://collector.example.com:4317", "other"), "Partial", []string{"serviceName"}},
	}
	for _, c := range cases {
		verdict, missing := JudgeGateway(c.tracing, grpcDest, "gw")
		if string(verdict) != c.want || !reflect.DeepEqual(missing, c.missing) {
			t.Errorf("%s: %s %v, want %s %v", c.name, verdict, missing, c.want, c.missing)
		}
	}
}

// A healthy managed gateway reports RUNNING. Reading that as a rollout in progress deferred every gateway
// forever, leaving it "Marked for instrumentation" with no reason given — found on a live organization.
func TestGatewayInFlight(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		g    anypoint.Gateway
		want bool
	}{
		{anypoint.Gateway{Status: "RUNNING", Ready: &yes}, false},
		{anypoint.Gateway{Status: "RUNNING"}, false},
		{anypoint.Gateway{Status: "APPLYING"}, true},
		{anypoint.Gateway{Status: "updating"}, true},
		{anypoint.Gateway{Status: "RUNNING", Ready: &no}, true},
		{anypoint.Gateway{Status: "FAILED", Ready: &no}, false},
	}
	for _, c := range cases {
		if got := gatewayInFlight(c.g); got != c.want {
			t.Errorf("gatewayInFlight(%s, ready=%v) = %v, want %v", c.g.Status, c.g.Ready, got, c.want)
		}
	}
}

// A gateway still applying an earlier change is left alone until it settles, then instrumented.
func TestAGatewayMidUpdateIsDeferredThenInstrumented(t *testing.T) {
	st := fake.SampleState()
	st.Gateways[fake.FixtureEnvSandbox][0]["status"], st.Gateways[fake.FixtureEnvSandbox][0]["ready"] = "APPLYING", false
	h := newGatewayHarness(t, st)
	ctx := context.Background()
	h.enable(fake.FixtureManagedGateway)
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(h.srv.Handler().GatewayUpdates()); n != 0 {
		t.Fatalf("updates while the gateway was applying = %d, want 0", n)
	}
	h.srv.Handler().Settle()
	if err := h.rec.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if !h.inv.apps[fake.FixtureManagedGateway].Instrumented {
		t.Error("gateway not instrumented once it settled")
	}
}

// A trial organization may not turn gateway tracing on (seen on a live organization: HTTP 403, "violates Trial
// Account restrictions"). The reason reaches the workload as its error, the gateway is unchanged, and the
// snapshot taken for the rejected write is not left behind for a later rollback to restore.
func TestARejectedGatewayWriteIsReportedAndLeavesNothing(t *testing.T) {
	h := newGatewayHarness(t, fake.SampleState())
	h.srv.Handler().TrialAccount = true
	h.enable(fake.FixtureManagedGateway)
	err := h.rec.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Trial Account") {
		t.Fatalf("reconcile = %v, want Anypoint's trial-account rejection", err)
	}
	app := h.inv.apps[fake.FixtureManagedGateway]
	if !strings.Contains(app.InstrumentationError, "Trial Account") {
		t.Errorf("workload error = %q, want the rejection", app.InstrumentationError)
	}
	if app.GatewaySnapshot != nil || app.InstrumentationPhase != "" || app.Instrumented {
		t.Errorf("after a rejected write: %+v, want no snapshot, no phase, not instrumented", app)
	}
	if tr := h.tracing(t); tr != nil {
		t.Errorf("gateway tracing = %v, want unchanged (none)", tr)
	}
}
