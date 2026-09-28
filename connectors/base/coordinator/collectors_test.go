package coordinator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const collectorKind = "cloud.fargate-collector"

// collectorSpec declares one collectors type on both features, so the customer's capability is what
// decides the mode rather than the provider.
var collectorSpec = spec.CloudConnectorSpec{
	Provider: "cloud",
	Collectors: spec.CollectorDomain{
		Management: spec.Feature{Enabled: true, Types: []spec.Type{{Name: collectorKind}}},
		Deployment: spec.Feature{
			Enabled: true,
			Types:   []spec.Type{{Name: collectorKind, Method: spec.MethodFargateService}},
		},
	},
}

// collectorProvider is a fakeProvider that also implements the optional Collector surface.
type collectorProvider struct {
	fakeProvider
	observers map[string]api.CollectorObserver
	deployers map[string]api.CollectorDeployer
}

func (p collectorProvider) Spec() spec.CloudConnectorSpec { return collectorSpec }
func (p collectorProvider) CollectorObservers() map[string]api.CollectorObserver {
	return p.observers
}
func (p collectorProvider) CollectorDeployers() map[string]api.CollectorDeployer {
	return p.deployers
}

// DesiredConfig makes this a complete provider. A provider that deploys but renders nothing is refused
// by activeCollectorHandlers, so a fixture without it would be testing that refusal and nothing else.
func (p collectorProvider) DesiredConfig(context.Context, store.CollectorKey) (collectorconfig.Document, error) {
	return collectorconfig.Document{}, nil
}

// recorder is an observer and a deployer that records every call, and can be told to fail or panic on a
// named network.
type recorder struct {
	mu sync.Mutex

	networks  []api.CollectorNetwork
	listErr   error
	observed  []string
	converged []string
	tornDown  []string
	failOn    map[string]error
	panicOn   map[string]bool
}

func newRecorder(networks ...string) *recorder {
	r := &recorder{failOn: map[string]error{}, panicOn: map[string]bool{}}
	for _, id := range networks {
		r.networks = append(r.networks, api.CollectorNetwork{
			Region: "us-east-1", NetworkID: id, Kind: collectorKind,
		})
	}
	return r
}

func (r *recorder) CollectorNetworks(context.Context) ([]api.CollectorNetwork, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.networks, nil
}

func (r *recorder) ObserveCollector(_ context.Context, n api.CollectorNetwork) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observed = append(r.observed, n.NetworkID)
	if r.panicOn[n.NetworkID] {
		panic("observer exploded on " + n.NetworkID)
	}
	return r.failOn[n.NetworkID]
}

func (r *recorder) ConvergeCollector(_ context.Context, n api.CollectorNetwork) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.converged = append(r.converged, n.NetworkID)
	if r.panicOn[n.NetworkID] {
		panic("deployer exploded on " + n.NetworkID)
	}
	return r.failOn[n.NetworkID]
}

func (r *recorder) TeardownCollector(_ context.Context, n api.CollectorNetwork) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tornDown = append(r.tornDown, n.NetworkID)
	return nil
}

func (r *recorder) calls() (observed, converged, tornDown []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.observed...), append([]string{}, r.converged...), append([]string{}, r.tornDown...)
}

// collectorCoordinator builds a Coordinator with the given capability and collector config.
func collectorCoordinator(t *testing.T, caps spec.Capabilities, collectorCfg map[string]any, rec *recorder) *Coordinator {
	t.Helper()

	provider := collectorProvider{
		observers: map[string]api.CollectorObserver{collectorKind: rec},
		deployers: map[string]api.CollectorDeployer{collectorKind: rec},
	}

	mgr := config.NewManager(&memoryConfigStore{}, config.MergeDefaults(config.Defaults(), nil))
	if collectorCfg != nil {
		setCollectorConfig(t, mgr, collectorCfg)
	}
	return New(config.Config{Capabilities: caps}, mgr, nil, provider)
}

// memoryConfigStore is the DB-backed operational config store, in memory. The tests drive overrides
// through Manager.Update — the same path agents-api's updateConfig takes — rather than reaching into the
// effective map, so "no restart needed" is proved end to end rather than asserted about a field.
type memoryConfigStore struct {
	mu        sync.Mutex
	overrides map[string]any
}

func (s *memoryConfigStore) GetConfig(context.Context) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overrides, nil
}

func (s *memoryConfigStore) SetConfig(_ context.Context, overrides map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides = overrides
	return nil
}

// setCollectorConfig applies a collector tree the way a customer would: one updateConfig patch.
func setCollectorConfig(t *testing.T, mgr *config.Manager, collectorCfg map[string]any) {
	t.Helper()
	if err := mgr.Update(context.Background(), map[string]any{collectorsettings.Key: collectorCfg}); err != nil {
		t.Fatalf("updateConfig: %v", err)
	}
}

func enabledConfig(regional map[string]any) map[string]any {
	return map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{
			"us-east-1": regional,
		},
	}
}

func automaticCaps() spec.Capabilities {
	return spec.Capabilities{Collectors: []spec.CollectorCapability{
		{Type: collectorKind, Management: true, Deployment: true},
	}}
}

func manualCaps() spec.Capabilities {
	return spec.Capabilities{Collectors: []spec.CollectorCapability{
		{Type: collectorKind, Management: true},
	}}
}

// TestManualModeNeverMutates is the criterion, and it holds because a manual-mode type has no deployer
// wired at all — not a deployer that refuses. There is no mutating call to make and no check to forget.
func TestManualModeNeverMutates(t *testing.T) {
	rec := newRecorder("vpc-1", "vpc-2")
	c := collectorCoordinator(t, manualCaps(), enabledConfig(nil), rec)

	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))
	if len(observers) != 1 {
		t.Fatalf("observers = %d, want 1 — manual mode is still configured over OpAMP", len(observers))
	}
	if len(deployers) != 0 {
		t.Fatalf("deployers = %v in manual mode; a manual-mode type must reach no mutating interface", deployers)
	}

	c.runCollectorReconcile(context.Background(), observers, deployers)

	observed, converged, tornDown := rec.calls()
	if len(observed) != 0 {
		t.Errorf("observed = %v, want no calls for an ambiguous regional host", observed)
	}
	if len(converged) != 0 || len(tornDown) != 0 {
		t.Errorf("manual mode mutated: converged %v, tore down %v", converged, tornDown)
	}
}

// TestAutomaticModeRequiresAnExplicitType: a deployer is wired only for a type the customer explicitly
// enabled deployment on. `management: true` alone is manual, whatever the provider can build.
func TestAutomaticModeRequiresAnExplicitType(t *testing.T) {
	cases := map[string]struct {
		caps         spec.Capabilities
		wantDeployer bool
	}{
		"management and deployment": {caps: automaticCaps(), wantDeployer: true},
		"management only":           {caps: manualCaps()},
		"no collectors capability":  {caps: spec.Capabilities{}},
		"a different type entirely": {caps: spec.Capabilities{Collectors: []spec.CollectorCapability{
			{Type: "cloud.some-other-collector", Management: true, Deployment: true},
		}}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := newRecorder("vpc-1")
			c := collectorCoordinator(t, tc.caps, enabledConfig(nil), rec)

			_, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))
			if got := len(deployers) > 0; got != tc.wantDeployer {
				t.Errorf("deployer wired = %v, want %v", got, tc.wantDeployer)
			}
		})
	}
}

// TestNoDeployerWithoutAnObserver: a type that can be built but not read back is refused. The observer
// is how a converge pass learns what already exists, and without it every pass would be a first pass.
func TestNoDeployerWithoutAnObserver(t *testing.T) {
	rec := newRecorder("vpc-1")
	provider := collectorProvider{
		observers: map[string]api.CollectorObserver{}, // none
		deployers: map[string]api.CollectorDeployer{collectorKind: rec},
	}
	c := New(config.Config{Capabilities: automaticCaps()},
		config.NewManager(&memoryConfigStore{}, config.Defaults()), nil, provider)

	observers, deployers := c.activeCollectorHandlers(provider)
	if len(observers) != 0 {
		t.Fatalf("observers = %v", observers)
	}
	if len(deployers) != 0 {
		t.Errorf("a deployer was wired for a type nothing observes: %v", deployers)
	}
}

// TestOverridesTakeEffectWithoutARestart: the loop reads the resolved configuration on every pass, so a
// change through updateConfig lands on the next tick — no pod restart, no CRD change.
func TestOverridesTakeEffectWithoutARestart(t *testing.T) {
	rec := newRecorder("vpc-1")
	c := collectorCoordinator(t, automaticCaps(), map[string]any{
		collectorsettings.KeyEnabled: false,
	}, rec)
	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))

	// Feature off: nothing happens, and nothing is deleted.
	c.runCollectorReconcile(context.Background(), observers, deployers)
	if observed, converged, _ := rec.calls(); len(observed) != 0 || len(converged) != 0 {
		t.Fatalf("the feature was off but the loop ran: observed %v converged %v", observed, converged)
	}

	// Turned on live, through the same manager the loop reads.
	setCollectorConfig(t, c.cfgMgr, enabledConfig(nil))
	c.runCollectorReconcile(context.Background(), observers, deployers)
	if _, converged, _ := rec.calls(); len(converged) != 1 {
		t.Errorf("converged = %v after enabling live, want vpc-1", converged)
	}

	// A regional disable lands on the next pass too, without deleting anything.
	setCollectorConfig(t, c.cfgMgr, map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{
			"us-east-1": map[string]any{collectorsettings.KeyEnabled: false},
		},
	})
	before, _, _ := rec.calls()
	c.runCollectorReconcile(context.Background(), observers, deployers)
	_, converged, tornDown := rec.calls()
	if len(converged) != 1 {
		t.Errorf("converged again after the region was disabled: %v", converged)
	}
	if len(tornDown) != 0 {
		t.Errorf("disabling a network tore it down: %v — it keeps running until somebody removes it", tornDown)
	}
	// Observation continues: a Collector that is still running is still reported.
	if observed, _, _ := rec.calls(); len(observed) <= len(before) {
		t.Error("a disabled network stopped being observed; it is still running and still reported")
	}
}

// TestRegionalHostIsObservedAndDeployedOnce verifies one regional lifecycle call
// even when the provider lists multiple candidate networks.
func TestSharedNetworkIsObservedButNotDeployed(t *testing.T) {
	rec := newRecorder("vpc-app", "vpc-shared")
	c := collectorCoordinator(t, automaticCaps(), map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{
			"us-east-1": map[string]any{collectorsettings.KeyHostNetworkID: "vpc-shared"},
		},
	}, rec)
	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))

	c.runCollectorReconcile(context.Background(), observers, deployers)

	observed, converged, _ := rec.calls()
	if len(observed) != 1 || observed[0] != "vpc-shared" {
		t.Errorf("observed = %v, want only regional host", observed)
	}
	if len(converged) != 1 || converged[0] != "vpc-shared" {
		t.Errorf("converged = %v, want regional host", converged)
	}
}

// TestAmbiguousRegionIsNotReconciled verifies the coordinator refuses to guess
// when the provider reports multiple possible hosts in one region.
func TestPerNetworkFailuresAreIsolated(t *testing.T) {
	rec := newRecorder("vpc-a", "vpc-b", "vpc-c")
	rec.failOn["vpc-b"] = errors.New("no egress route from any subnet")

	c := collectorCoordinator(t, automaticCaps(), enabledConfig(nil), rec)
	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))

	c.runCollectorReconcile(context.Background(), observers, deployers)

	_, converged, _ := rec.calls()
	if len(converged) != 0 {
		t.Errorf("converged = %v; ambiguous host was selected", converged)
	}
}

// TestAmbiguousRegionWithProviderPanicDoesNotCallProvider ensures ambiguity is
// resolved before observer/deployer calls.
func TestAPanickingProviderIsOneNetworksFailure(t *testing.T) {
	rec := newRecorder("vpc-a", "vpc-b", "vpc-c")
	rec.panicOn["vpc-b"] = true

	c := collectorCoordinator(t, automaticCaps(), enabledConfig(nil), rec)
	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))

	c.runCollectorReconcile(context.Background(), observers, deployers)

	observed, converged, _ := rec.calls()
	if len(observed) != 0 {
		t.Errorf("observed = %v; ambiguous host was selected", observed)
	}
	if len(converged) != 0 {
		t.Errorf("converged = %v, want no calls", converged)
	}
}

// TestAListingFailureCostsOnlyThatType: one observer that cannot enumerate its networks must not stop
// another type's from being reconciled.
func TestAListingFailureCostsOnlyThatType(t *testing.T) {
	broken := newRecorder("vpc-broken")
	broken.listErr = errors.New("describe call throttled")
	working := newRecorder("vpc-working")

	const otherKind = "cloud.other-collector"
	spec2 := collectorSpec
	spec2.Collectors = spec.CollectorDomain{
		Management: spec.Feature{Enabled: true, Types: []spec.Type{{Name: collectorKind}, {Name: otherKind}}},
		Deployment: spec.Feature{Enabled: true, Types: []spec.Type{
			{Name: collectorKind, Method: spec.MethodFargateService},
			{Name: otherKind, Method: spec.MethodFargateService},
		}},
	}

	provider := twoTypeProvider{
		spec: spec2,
		observers: map[string]api.CollectorObserver{
			collectorKind: broken, otherKind: working,
		},
		deployers: map[string]api.CollectorDeployer{
			collectorKind: broken, otherKind: working,
		},
	}

	caps := spec.Capabilities{Collectors: []spec.CollectorCapability{
		{Type: collectorKind, Management: true, Deployment: true},
		{Type: otherKind, Management: true, Deployment: true},
	}}
	mgr := config.NewManager(&memoryConfigStore{}, config.Defaults())
	setCollectorConfig(t, mgr, enabledConfig(nil))
	c := New(config.Config{Capabilities: caps}, mgr, nil, provider)

	observers, deployers := c.activeCollectorHandlers(provider)
	c.runCollectorReconcile(context.Background(), observers, deployers)

	if _, converged, _ := working.calls(); len(converged) != 1 {
		t.Errorf("the working type converged %v; a listing failure elsewhere stopped it", converged)
	}
	if _, converged, _ := broken.calls(); len(converged) != 0 {
		t.Errorf("the broken type converged %v despite listing nothing", converged)
	}
}

type twoTypeProvider struct {
	fakeProvider
	spec      spec.CloudConnectorSpec
	observers map[string]api.CollectorObserver
	deployers map[string]api.CollectorDeployer
}

func (p twoTypeProvider) Spec() spec.CloudConnectorSpec { return p.spec }
func (p twoTypeProvider) CollectorObservers() map[string]api.CollectorObserver {
	return p.observers
}
func (p twoTypeProvider) CollectorDeployers() map[string]api.CollectorDeployer {
	return p.deployers
}

func (p twoTypeProvider) DesiredConfig(context.Context, store.CollectorKey) (collectorconfig.Document, error) {
	return collectorconfig.Document{}, nil
}

// deployOnlyProvider can build Collectors and cannot configure them: api.CollectorProvider without
// opamp.Desired. The shape a provider takes halfway through being implemented.
type deployOnlyProvider struct {
	fakeProvider
	observers map[string]api.CollectorObserver
	deployers map[string]api.CollectorDeployer
}

func (p deployOnlyProvider) Spec() spec.CloudConnectorSpec { return collectorSpec }
func (p deployOnlyProvider) CollectorObservers() map[string]api.CollectorObserver {
	return p.observers
}
func (p deployOnlyProvider) CollectorDeployers() map[string]api.CollectorDeployer {
	return p.deployers
}

// TestNoDeploymentWithoutAConfigPlane: a provider that deploys Collectors but renders no configuration
// for them gets no deployer at all.
//
// This is the expensive failure, and it is why the check is not just a log line. The loop would create
// Collectors in the customer's cloud account, and with no DesiredConfig behind them each one boots on the
// inert bootstrap fallback and stays there — running, billed, exporting nothing, and reported healthy
// because it is. Capabilities.Validate refuses the same contradiction when a *capability* declares
// deployment without management; it cannot see a *provider* that declares both and implements one.
//
// Observation survives, because reading a Collector the customer deployed themselves is still useful.
func TestNoDeploymentWithoutAConfigPlane(t *testing.T) {
	rec := &recorder{networks: []api.CollectorNetwork{{Region: "r1", NetworkID: "vpc-1", Kind: collectorKind}}}
	p := deployOnlyProvider{
		observers: map[string]api.CollectorObserver{collectorKind: rec},
		deployers: map[string]api.CollectorDeployer{collectorKind: rec},
	}

	c := &Coordinator{
		cfg:      config.Config{Capabilities: automaticCaps()},
		provider: p,
	}

	observers, deployers := c.activeCollectorHandlers(p)
	if len(observers) != 1 {
		t.Fatalf("observers = %d, want the Collector still observed", len(observers))
	}
	if len(deployers) != 0 {
		t.Fatalf("deployers = %d, want none: this provider cannot configure what it would build", len(deployers))
	}
}

// TestAProviderWithoutTheCollectorSurfaceIsAbsent: the interfaces are optional, so a provider that has
// not built a Collector is simply not in the loop.
func TestAProviderWithoutTheCollectorSurfaceIsAbsent(t *testing.T) {
	c := New(config.Config{Capabilities: automaticCaps()},
		config.NewManager(&memoryConfigStore{}, config.Defaults()), nil, fakeProvider{})

	if _, ok := c.provider.(api.CollectorProvider); ok {
		t.Fatal("the plain fake provider implements the Collector surface; this test proves nothing")
	}
	// The loop returns immediately rather than panicking on the type assertion.
	c.collectorReconcileLoop(context.Background())
}

// TestObservationHappensEvenWhenItFails: a converge pass that has to create everything from scratch is
// still better than no pass, and the deployer reads its own state anyway.
func TestObservationHappensEvenWhenItFails(t *testing.T) {
	rec := newRecorder("vpc-1")
	rec.failOn["vpc-1"] = errors.New("describe throttled")

	c := collectorCoordinator(t, automaticCaps(), enabledConfig(nil), rec)
	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))
	c.runCollectorReconcile(context.Background(), observers, deployers)

	observed, converged, _ := rec.calls()
	if len(observed) != 1 || len(converged) != 1 {
		t.Errorf("observed %v converged %v; a failed observation must not skip the converge", observed, converged)
	}
}

// TestDrainingStopsTheLoop: the shutdown drain rolls instrumentation back, and a reconcile pass racing
// it would re-create what the drain just removed. The mutation gate holds during the drain too, because
// a manual-mode type has no deployer to reach in the first place.
func TestDrainingStopsTheLoop(t *testing.T) {
	rec := newRecorder("vpc-1")
	c := collectorCoordinator(t, automaticCaps(), enabledConfig(nil), rec)
	observers, deployers := c.activeCollectorHandlers(c.provider.(api.CollectorProvider))

	c.draining.Store(true)
	c.runCollectorReconcile(context.Background(), observers, deployers)

	if observed, converged, _ := rec.calls(); len(observed) != 0 || len(converged) != 0 {
		t.Errorf("the loop ran during a drain: observed %v converged %v", observed, converged)
	}
}

// TestCollectorSettingsAreReadLive: the accessor a provider deployer and the reporting path use must
// reflect a change made through updateConfig without anything being rebuilt.
func TestCollectorSettingsAreReadLive(t *testing.T) {
	c := collectorCoordinator(t, automaticCaps(), map[string]any{collectorsettings.KeyEnabled: false}, newRecorder())

	if c.CollectorSettings().Enabled {
		t.Fatal("settings resolved as enabled")
	}
	setCollectorConfig(t, c.cfgMgr, enabledConfig(map[string]any{collectorsettings.KeyMemory: "4096"}))
	settings := c.CollectorSettings()
	if !settings.Enabled {
		t.Error("a live change was not visible")
	}
	if got := settings.Region("us-east-1").Memory; got != "4096" {
		t.Errorf("memory = %q, want the live override", got)
	}
}
