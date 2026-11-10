package detector

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/anypoint"
	"github.com/odigos-io/odigos/connectors/anypoint/fake"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

type memInventory struct {
	apps   map[string]store.Application
	pruned map[string]store.PruneScope
}

func newMem() *memInventory {
	return &memInventory{apps: map[string]store.Application{}, pruned: map[string]store.PruneScope{}}
}

func (m *memInventory) UpsertApplication(_ context.Context, a store.Application) error {
	m.apps[a.ExternalID] = a
	return nil
}
func (m *memInventory) UpdateDiscoveryState(context.Context, string, string, time.Time) error {
	return nil
}
func (m *memInventory) Prune(_ context.Context, kind string, _ time.Time, scope store.PruneScope) error {
	m.pruned[kind] = scope
	return nil
}
func (m *memInventory) PurgeDeleted(context.Context, time.Time) error { return nil }
func (m *memInventory) Now(context.Context) (time.Time, error)        { return time.Now(), nil }
func (m *memInventory) KnownTargets(context.Context) (map[string]store.KnownTarget, error) {
	out := map[string]store.KnownTarget{}
	for id, a := range m.apps {
		out[id] = store.KnownTarget{Kind: a.Kind, TargetName: a.TargetName, TargetType: a.TargetType, TargetRegion: a.TargetRegion}
	}
	return out, nil
}

func client(srv *fake.Server) *anypoint.Client {
	return anypoint.New(anypoint.Config{BaseURL: srv.URL, ClientID: fake.FixtureClientID, ClientSecret: fake.FixtureClientSecret, Backoff: time.Millisecond})
}

func TestAMCDetectorsSplitOneScanByKind(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	scanner := NewAMCScanner(client(srv), fake.FixtureOrgID)
	mem := newMem()
	ctx := context.Background()

	for _, kind := range []string{store.KindCloudHub2, store.KindPrivateSpace, store.KindRTF} {
		if _, err := NewAMCDetector(kind, scanner, mem).Detect(ctx); err != nil {
			t.Fatalf("detect %s: %v", kind, err)
		}
		if s, ok := mem.pruned[kind]; !ok || len(s.Complete) == 0 || s.Listed == nil {
			t.Errorf("%s: a complete scan should prune its own kind everywhere, got %+v", kind, s)
		}
	}

	rtf := mem.apps[fake.FixtureRTFApp]
	if rtf.Kind != store.KindRTF || rtf.TargetType != anypoint.TargetRuntimeFab || rtf.TargetRegion != "eu-west-1" {
		t.Errorf("rtf app = %+v, want an RTF row on the fabric", rtf)
	}
	private := mem.apps[fake.FixtureInventory]
	if private.Kind != store.KindPrivateSpace || private.TargetType != anypoint.TargetPrivateSpace {
		t.Errorf("private-space app = %+v, want a private-space row", private)
	}
	if shared := mem.apps[fake.FixtureOrderSandbox]; shared.Kind != store.KindCloudHub2 || shared.TargetType != anypoint.TargetSharedSpace {
		t.Errorf("shared-space app = %+v, want a CloudHub 2.0 row", shared)
	}
	if got := len(mem.apps); got != 6 {
		t.Errorf("applications = %d, want 6", got)
	}
}

// TestSameNameInTwoEnvironmentsIsTwoWorkloads: the report key is name + kind + region, and region carries the
// environment, so the same app in Sandbox and Production must surface as two workloads.
func TestSameNameInTwoEnvironmentsIsTwoWorkloads(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	mem := newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(srv), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	sandbox := store.ToDiscoveredWorkload(mem.apps[fake.FixtureOrderSandbox], time.Now())
	prod := store.ToDiscoveredWorkload(mem.apps[fake.FixtureOrderProduction], time.Now())
	if sandbox.ServiceName != prod.ServiceName {
		t.Fatalf("fixture expects the same name, got %q and %q", sandbox.ServiceName, prod.ServiceName)
	}
	if sandbox.Region == prod.Region {
		t.Errorf("both report region %q; the environment must tell them apart", sandbox.Region)
	}
	if sandbox.RuntimeLanguage != "mule" || prod.RuntimeVersion != "4.11.2" {
		t.Errorf("runtime = %s %s, want mule 4.11.2", prod.RuntimeLanguage, prod.RuntimeVersion)
	}
}

func TestMarkersSetTheOrigin(t *testing.T) {
	st := fake.SampleState()
	sandbox := st.Deployments[fake.FixtureEnvSandbox]
	sandbox[0].Application.Configuration.PropertiesService.Properties[store.PropOdigosManaged] = `{"v":1,"prevProps":{}}`
	sandbox[1].Application.Configuration.PropertiesService.SecureProperties = map[string]string{store.PropOdigosIaC: ""}
	srv := fake.NewServer(st)
	defer srv.Close()
	mem := newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(srv), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mem.apps[sandbox[0].ID].Origin; got != store.OriginConnector {
		t.Errorf("connector-marked origin = %q", got)
	}
	if got := mem.apps[sandbox[1].ID].Origin; got != store.OriginExternal {
		t.Errorf("IaC-marked origin = %q (a secure-property marker still counts)", got)
	}
	if got := mem.apps[sandbox[2].ID].Origin; got != "" {
		t.Errorf("a customer's own telemetry properties are not an Odigos marker, got origin %q", got)
	}
}

// TestUnreadableEnvironmentIsNotPruned: an environment the Connected App cannot read keeps its rows, and does not
// stop the readable environments from being pruned.
func TestUnreadableEnvironmentIsNotPruned(t *testing.T) {
	st := fake.SampleState()
	st.Forbidden = []string{"/amc/application-manager/api/v2/organizations/" + fake.FixtureOrgID + "/environments/" + fake.FixtureEnvProduction}
	srv := fake.NewServer(st)
	defer srv.Close()
	mem := newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(srv), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope := mem.pruned[store.KindCloudHub2]
	if slices.Contains(scope.Complete, fake.FixtureEnvProduction) {
		t.Error("an environment that could not be read must not have its applications pruned")
	}
	if !slices.Contains(scope.Complete, fake.FixtureEnvSandbox) {
		t.Errorf("the readable environment should still be pruned, scope = %+v", scope)
	}
	if _, ok := mem.apps[fake.FixtureOrderSandbox]; !ok {
		t.Error("the readable environment should still be discovered")
	}
}

func TestUnreadableTargetsFallBackToTargetIDShape(t *testing.T) {
	st := fake.SampleState()
	st.Forbidden = []string{"/runtimefabric/"}
	srv := fake.NewServer(st)
	defer srv.Close()
	mem := newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(srv), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatalf("a missing targets scope must degrade classification, not fail discovery: %v", err)
	}
	if a := mem.apps[fake.FixtureOrderSandbox]; a.TargetType != anypoint.TargetSharedSpace || a.TargetRegion != "us-east-2" {
		t.Errorf("shared-space app = %+v, want it classified from its cloudhub-<region> id", a)
	}
}

func TestHybridDetectorReadsServerVersions(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	mem := newMem()
	if _, err := NewHybridDetector(client(srv), fake.FixtureOrgID, mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, ok := mem.apps[HybridExternalID(fake.FixtureEnvProduction, 4101)]
	if !ok {
		t.Fatalf("hybrid app not discovered: %v", mem.apps)
	}
	if a.Kind != store.KindHybrid || a.RuntimeVersion != "4.11.1" || a.EnvironmentName != "Production" {
		t.Errorf("hybrid app = %+v", a)
	}
	if w := store.ToDiscoveredWorkload(a, time.Now()); w.InstrumentationType != "" {
		t.Errorf("hybrid apps have no per-app instrumentation type, got %q", w.InstrumentationType)
	}
}

// TestTransientTargetsFailureKeepsRuntimeFabricApps: one failed targets call must not relabel a Runtime Fabric
// app as CloudHub 2.0 — that would orphan its RTF source and make the CloudHub 2.0 reconciler roll it back.
func TestTransientTargetsFailureKeepsRuntimeFabricApps(t *testing.T) {
	good := fake.NewServer(fake.SampleState())
	defer good.Close()
	mem := newMem()
	for _, kind := range []string{store.KindCloudHub2, store.KindPrivateSpace, store.KindRTF} {
		if _, err := NewAMCDetector(kind, NewAMCScanner(client(good), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	st := fake.SampleState()
	st.Forbidden = []string{"/runtimefabric/"}
	flaky := fake.NewServer(st)
	defer flaky.Close()
	scanner := NewAMCScanner(client(flaky), fake.FixtureOrgID)
	for _, kind := range []string{store.KindCloudHub2, store.KindPrivateSpace, store.KindRTF} {
		if _, err := NewAMCDetector(kind, scanner, mem).Detect(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	rtf := mem.apps[fake.FixtureRTFApp]
	if rtf.Kind != store.KindRTF || rtf.TargetType != anypoint.TargetRuntimeFab || rtf.TargetRegion != "eu-west-1" {
		t.Errorf("rtf app after a failed targets call = %+v, want it still classified as Runtime Fabric", rtf)
	}
	if p := mem.apps[fake.FixtureInventory]; p.Kind != store.KindPrivateSpace || p.TargetType != anypoint.TargetPrivateSpace {
		t.Errorf("private-space app = %+v, want its recorded private-space target kept", p)
	}
}

// TestTruncatedListDiscoversButNeverPrunes: a list shorter than its reported total may be missing instrumented
// applications; pruning them would make the reconciler roll them back.
func TestTruncatedListDiscoversButNeverPrunes(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	srv.Handler().InflateTotals = 3
	mem := newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(srv), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, ok := mem.pruned[store.KindCloudHub2]; ok {
		t.Errorf("a truncated list must not prune, got scope %+v", s)
	}
	if _, ok := mem.apps[fake.FixtureOrderSandbox]; !ok {
		t.Error("the applications that were returned should still be discovered")
	}
}

// TestInstrumentedApplicationKeepsItsKind: a Runtime Fabric app first seen while the targets list was unreadable
// is recorded under the private-space guess. Once Odigos has instrumented it under that kind, a later correct
// classification must not move the row away from the reconciler that owns its marker; it moves once the marker
// is gone.
func TestInstrumentedApplicationKeepsItsKind(t *testing.T) {
	st := fake.SampleState()
	st.Forbidden = []string{"/runtimefabric/"}
	blind := fake.NewServer(st)
	defer blind.Close()
	mem := newMem()
	if _, err := NewAMCDetector(store.KindPrivateSpace, NewAMCScanner(client(blind), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if k := mem.apps[fake.FixtureRTFApp].Kind; k != store.KindPrivateSpace {
		t.Fatalf("setup: rtf app first recorded as %q, want the private-space guess", k)
	}

	good := fake.NewServer(fake.SampleState())
	defer good.Close()
	env, dep := findDeployment(t, fake.SampleState(), fake.FixtureRTFApp)
	marked := dep // the configuration is behind pointers: give the marked copy its own
	marked.Application.Configuration = &anypoint.ApplicationConfiguration{PropertiesService: &anypoint.PropertiesService{
		ApplicationName: dep.Name, Properties: map[string]string{store.PropOdigosManaged: `{"v":1,"prevProps":{}}`},
	}}
	good.Handler().SetDeployment(env, marked)
	detectBoth := func() {
		scanner := NewAMCScanner(client(good), fake.FixtureOrgID)
		for _, kind := range []string{store.KindPrivateSpace, store.KindRTF} {
			if _, err := NewAMCDetector(kind, scanner, mem).Detect(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	detectBoth()
	if k := mem.apps[fake.FixtureRTFApp].Kind; k != store.KindPrivateSpace {
		t.Errorf("instrumented app moved to %q while Odigos's marker is on it", k)
	}

	good.Handler().SetDeployment(env, dep) // rolled back
	detectBoth()
	if k := mem.apps[fake.FixtureRTFApp].Kind; k != store.KindRTF {
		t.Errorf("after rollback the app is %q, want it reclassified as Runtime Fabric", k)
	}
}

func findDeployment(t *testing.T, st fake.State, id string) (string, anypoint.Deployment) {
	t.Helper()
	for env, deps := range st.Deployments {
		for _, d := range deps {
			if d.ID == id {
				return env, d
			}
		}
	}
	t.Fatalf("deployment %s not in the fixture", id)
	return "", anypoint.Deployment{}
}

// TestOnlyAKnownSharedSpaceIsCloudHub: CloudHub 2.0 is the one kind Odigos instruments, so a target lands in it
// only when it is known to be a shared space. Whatever cannot be placed goes to the private-space kind, whose
// instrumentation is declared unimplemented, rather than being instrumented on a guess.
func TestOnlyAKnownSharedSpaceIsCloudHub(t *testing.T) {
	targets := map[string]anypoint.Target{
		"cloudhub-us-east-2": {ID: "cloudhub-us-east-2", Type: "shared-space"},
		"ps-1":               {ID: "ps-1", Type: "private-space"},
		"rtf-1":              {ID: "rtf-1", Type: "runtime-fabric"},
		"odd-1":              {ID: "odd-1", Type: "something-new"},
		"cloudhub-eu-west-1": {ID: "cloudhub-eu-west-1", Type: "something-new"},
	}
	cases := []struct {
		target      string
		kind        string
		wantGuessed bool
	}{
		{"cloudhub-us-east-2", store.KindCloudHub2, false},
		{"ps-1", store.KindPrivateSpace, false},
		{"rtf-1", store.KindRTF, false},
		{"odd-1", store.KindPrivateSpace, false},
		{"cloudhub-eu-west-1", store.KindCloudHub2, false},
		{"cloudhub-ap-southeast-1", store.KindCloudHub2, true},
		{"6f1c2a9e-0000-4000-8000-000000000001", store.KindPrivateSpace, true},
	}
	for _, c := range cases {
		_, kind, guessed := classify(c.target, targets)
		if kind != c.kind || guessed != c.wantGuessed {
			t.Errorf("classify(%q) = %s guessed=%v, want %s guessed=%v", c.target, kind, guessed, c.kind, c.wantGuessed)
		}
	}
}

// TestAPIManagerPairingIsRecordedOnlyFromARead: an application paired through autodiscovery records its API
// Manager instance, an unpaired one records that it has none, and when API Manager cannot be read (a missing
// scope) nothing is recorded at all, so the store keeps the last pairing rather than flipping an instrumented
// application's attributes — every change of which is a redeploy.
func TestAPIManagerPairingIsRecordedOnlyFromARead(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	mem := newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(srv), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	paired, _ := mem.apps[fake.FixtureOrderProduction].Metadata[store.MetaAPI].(*store.APIRef)
	if paired == nil || paired.ID != fake.FixtureOrderAPIInstance || paired.AssetID != "order-api" || paired.Version != "v1" {
		t.Fatalf("production order-api pairing = %+v, want instance %s", paired, fake.FixtureOrderAPIInstance)
	}
	if v, has := mem.apps[fake.FixtureOrderSandbox].Metadata[store.MetaAPI]; !has || v.(*store.APIRef) != nil {
		t.Errorf("sandbox order-api pairing = %v (recorded %v), want an explicit none", v, has)
	}

	st := fake.SampleState()
	st.Forbidden = []string{"/apimanager/"}
	blind := fake.NewServer(st)
	defer blind.Close()
	mem = newMem()
	if _, err := NewAMCDetector(store.KindCloudHub2, NewAMCScanner(client(blind), fake.FixtureOrgID), mem).Detect(context.Background()); err != nil {
		t.Fatalf("an unreadable API Manager must not fail discovery: %v", err)
	}
	for id, app := range mem.apps {
		if _, has := app.Metadata[store.MetaAPI]; has {
			t.Errorf("%s: pairing recorded without an API Manager read", id)
		}
	}
}

func TestPairedAPIMatchesOnlyAnAPIIDPropertyInTheSameEnvironment(t *testing.T) {
	apis := []anypoint.APIInstance{{ID: "500", AssetID: "orders", ProductVersion: "v2"}}
	cases := []struct {
		name  string
		props map[string]string
		want  string
	}{
		{"api.id", map[string]string{"api.id": "500"}, "500"},
		{"a prefixed key", map[string]string{"orders.api.id": " 500 "}, "500"},
		{"camel case", map[string]string{"apiId": "500"}, "500"},
		{"an id of another environment", map[string]string{"api.id": "501"}, ""},
		{"the right number under another name", map[string]string{"http.port": "500"}, ""},
		{"no properties", nil, ""},
	}
	for _, c := range cases {
		got := PairedAPI(c.props, apis)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: paired with %+v, want none", c.name, got)
		case c.want != "" && (got == nil || got.ID != c.want || got.Version != "v2"):
			t.Errorf("%s: paired with %+v, want %s", c.name, got, c.want)
		}
	}
}

// TestGatewaysAreDiscoveredWithTheirMarkers: both gateway flavors are discovered, the managed one is read in
// full so its Odigos label counts, and a self-managed one — whose configuration is on its host — has no origin.
func TestGatewaysAreDiscoveredWithTheirMarkers(t *testing.T) {
	st := fake.SampleState()
	st.Gateways[fake.FixtureEnvSandbox][0]["configuration"].(map[string]any)["tracing"] = map[string]any{
		"enabled": true,
		"labels":  []any{map[string]any{"name": store.GatewayIaCLabel, "type": "literal", "defaultValue": "omni-gateway"}},
	}
	srv := fake.NewServer(st)
	defer srv.Close()
	mem := newMem()
	if _, err := NewGatewayDetector(client(srv), fake.FixtureOrgID, mem).Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	managed, self := mem.apps[fake.FixtureManagedGateway], mem.apps[fake.FixtureSelfGateway]
	if managed.Kind != store.KindOmniGateway || managed.TargetType != store.GatewayManaged || managed.Origin != store.OriginExternal ||
		managed.EnvironmentName != "Sandbox" || managed.RuntimeVersion != "1.10.2" {
		t.Errorf("managed gateway = %+v", managed)
	}
	if self.Kind != store.KindOmniGateway || self.TargetType != store.GatewaySelfManaged || self.Origin != "" {
		t.Errorf("self-managed gateway = %+v", self)
	}
	if s, ok := mem.pruned[store.KindOmniGateway]; !ok || len(s.Complete) != 2 {
		t.Errorf("prune scope = %+v, want both environments read", s)
	}
}
