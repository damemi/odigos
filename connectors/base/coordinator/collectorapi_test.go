package coordinator

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

type memoryCollectorResourceStore struct {
	mu      sync.Mutex
	states  map[string]store.CollectorResourceState
	records map[string]store.CollectorRecord
}

func newMemoryCollectorResourceStore() *memoryCollectorResourceStore {
	return &memoryCollectorResourceStore{states: map[string]store.CollectorResourceState{}, records: map[string]store.CollectorRecord{}}
}

func (m *memoryCollectorResourceStore) GetCollectorResourceState(_ context.Context, region string) (store.CollectorResourceState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[region]
	return state, ok, nil
}

func (m *memoryCollectorResourceStore) ListCollectorResourceStates(context.Context) (map[string]store.CollectorResourceState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]store.CollectorResourceState, len(m.states))
	for region, state := range m.states {
		out[region] = state
	}
	return out, nil
}

func (m *memoryCollectorResourceStore) SetCollectorResourceState(_ context.Context, region string, state store.CollectorResourceState, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[region] = state
	return nil
}

func (m *memoryCollectorResourceStore) GetManagedCollector(_ context.Context, key store.CollectorKey) (store.CollectorRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[key.Region]
	if !ok {
		return store.CollectorRecord{}, store.ErrCollectorNotFound
	}
	return rec, nil
}

func (m *memoryCollectorResourceStore) ListManagedCollectors(context.Context) ([]store.CollectorRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.CollectorRecord, 0, len(m.records))
	for _, rec := range m.records {
		out = append(out, rec)
	}
	return out, nil
}

func (m *memoryCollectorResourceStore) DeleteManagedCollector(_ context.Context, key store.CollectorKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key.Region)
	return nil
}

func (m *memoryCollectorResourceStore) DeleteCollectorDestinationsForRegion(context.Context, string) error {
	return nil
}

func TestUnprovisionedCollectorTeardownRemovesDesiredAndRecordedState(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		c := newTestCoordinator(t)
		c.cfg.Capabilities = automaticCaps()
		c.provider = collectorProvider{}
		resources := newMemoryCollectorResourceStore()
		c.collectorResources = resources
		region := "us-east-1"
		enabled := true
		if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
			return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &enabled})
		}); err != nil {
			t.Fatal(err)
		}
		resources.states[region] = store.CollectorResourcesNotCreated
		if recorded {
			resources.records[region] = store.CollectorRecord{Key: store.CollectorKey{Region: region}, Mode: spec.ModeAutomatic, Origin: "odigos"}
		}
		report, err := c.ManagedCollectorReport(context.Background(), region)
		if err != nil || report == nil || !report.Deletable {
			t.Fatalf("recorded=%v: unprovisioned report = %+v, %v", recorded, report, err)
		}

		result, err := c.TeardownManagedCollector(context.Background(), region)
		if err != nil || !result.Deleted {
			t.Fatalf("recorded=%v: teardown = %+v, %v", recorded, result, err)
		}
		if collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
			t.Fatalf("recorded=%v: desired region survived teardown", recorded)
		}
		if _, ok := resources.records[region]; ok {
			t.Fatalf("recorded=%v: Collector row survived teardown", recorded)
		}
		if resources.states[region] != store.CollectorResourcesDestroyed {
			t.Fatalf("recorded=%v: resource state = %s", recorded, resources.states[region])
		}
	}
}

func TestLegacyCollectorWithoutCloudParametersCanBeDeleted(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	c.provider = collectorProvider{}
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	region := "us-east-1"
	resources.records[region] = store.CollectorRecord{Key: store.CollectorKey{Region: region}, Mode: spec.ModeAutomatic, Origin: "odigos"}
	enabled := true
	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatal(err)
	}

	report, err := c.ManagedCollectorReport(context.Background(), region)
	if err != nil || report == nil || !report.Deletable {
		t.Fatalf("legacy report = %+v, %v", report, err)
	}
	result, err := c.TeardownManagedCollector(context.Background(), region)
	if err != nil || !result.Deleted {
		t.Fatalf("legacy teardown = %+v, %v", result, err)
	}
	if collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
		t.Fatal("legacy declaration survived teardown")
	}
	if _, ok := resources.records[region]; ok {
		t.Fatal("legacy Collector row survived teardown")
	}
	if resources.states[region] != store.CollectorResourcesDestroyed {
		t.Fatalf("legacy resource state = %s", resources.states[region])
	}
}

func TestRecordedCloudReferencesCannotBeDiscardedLocally(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	c.provider = collectorProvider{}
	rec := store.CollectorRecord{Key: store.CollectorKey{Region: "us-east-1"}, Mode: spec.ModeAutomatic, Origin: "odigos", WorkloadRef: "service-arn"}
	for _, state := range []store.CollectorResourceState{store.CollectorResourcesNotCreated, store.CollectorResourcesDestroyed} {
		if c.canDiscardCollectorState(rec.Key.Region, &rec, state, true) {
			t.Fatalf("recorded cloud infrastructure was eligible for local-only cleanup in state %s", state)
		}
	}
}

func TestTeardownRetainingInfrastructureRecordIsOrphaned(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	deployer := newRecorder()
	c.provider = collectorProvider{deployers: map[string]api.CollectorDeployer{collectorKind: deployer}}
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	region := "us-east-1"
	resources.records[region] = store.CollectorRecord{
		Key: store.CollectorKey{Region: region}, NetworkID: "vpc-1", Kind: collectorKind,
		Mode: spec.ModeAutomatic, Origin: "odigos", WorkloadRef: "service-arn",
	}
	resources.states[region] = store.CollectorResourcesCreated

	result, err := c.TeardownManagedCollector(context.Background(), region)
	if err != nil || result.Deleted {
		t.Fatalf("retained teardown = %+v, %v", result, err)
	}
	if resources.states[region] != store.CollectorResourcesOrphaned {
		t.Fatalf("retained resource state = %s", resources.states[region])
	}
	if _, ok := resources.records[region]; !ok {
		t.Fatal("incomplete teardown lost the infrastructure record")
	}
}

func TestShutdownDrainRemovesUnprovisionedDeclaration(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	c.provider = collectorProvider{deployers: map[string]api.CollectorDeployer{collectorKind: newRecorder()}}
	c.collectorRows = fakeCollectorRows{}
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	region := "us-east-1"
	resources.states[region] = store.CollectorResourcesNotCreated
	enabled := true
	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if collectorsettings.HasRegion(c.storedCollectorSettingsTree(), region) {
		t.Fatal("shutdown left an unprovisioned declaration behind")
	}
	if resources.states[region] != store.CollectorResourcesDestroyed {
		t.Fatalf("shutdown state = %s", resources.states[region])
	}
}

func TestShutdownDrainRetainsIncompleteInfrastructure(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	c.provider = collectorProvider{deployers: map[string]api.CollectorDeployer{collectorKind: newRecorder()}}
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	region := "us-east-1"
	rec := store.CollectorRecord{
		Key: store.CollectorKey{Region: region}, NetworkID: "vpc-1", Kind: collectorKind,
		Mode: spec.ModeAutomatic, Origin: "odigos", WorkloadRef: "service-arn",
	}
	resources.records[region] = rec
	resources.states[region] = store.CollectorResourcesCreated
	c.collectorRows = fakeCollectorRows{records: []store.CollectorRecord{rec}}
	if err := c.drainCollectors(context.Background()); err == nil {
		t.Fatal("shutdown reported complete while infrastructure remained recorded")
	}
	if resources.states[region] != store.CollectorResourcesOrphaned {
		t.Fatalf("incomplete shutdown state = %s", resources.states[region])
	}
}

func TestUnknownCloudResourcesCannotBeDiscardedAsLocalState(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	c.provider = collectorProvider{}
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	resources.states["us-east-1"] = store.CollectorResourcesProvisioning
	enabled := true
	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, "us-east-1", collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatal(err)
	}

	result, err := c.TeardownManagedCollector(context.Background(), "us-east-1")
	if err != nil || result.Deleted {
		t.Fatalf("uncertain cloud state was deleted: %+v, %v", result, err)
	}
	if !collectorsettings.HasRegion(c.storedCollectorSettingsTree(), "us-east-1") {
		t.Fatal("uncertain cloud state lost its desired declaration")
	}
	report, err := c.ManagedCollectorReport(context.Background(), "us-east-1")
	if err != nil || report == nil || report.Deletable {
		t.Fatalf("uncertain cloud state report = %+v, %v", report, err)
	}
	if resources.states["us-east-1"] != store.CollectorResourcesOrphaned {
		t.Fatalf("uncertain state was not retained as orphaned: %s", resources.states["us-east-1"])
	}
}

func TestOrphanedCloudStateRemainsVisibleWithoutConfigurationOrProviderRow(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	c.provider = collectorProvider{}
	resources := newMemoryCollectorResourceStore()
	resources.states["us-east-1"] = store.CollectorResourcesOrphaned
	c.collectorResources = resources

	reports, err := c.ManagedCollectorReports(context.Background())
	if err != nil || len(reports) != 1 || reports[0].Region != "us-east-1" || reports[0].Deletable {
		t.Fatalf("orphaned resource reports = %+v, %v", reports, err)
	}
	if len(reports[0].Problems) != 1 || reports[0].Problems[0].Code != "CollectorCloudResourcesOrphaned" {
		t.Fatalf("orphaned resource diagnosis = %+v", reports[0].Problems)
	}
	result, err := c.TeardownManagedCollector(context.Background(), "us-east-1")
	if err != nil || result.Deleted {
		t.Fatalf("orphaned resource was reported deleted: %+v, %v", result, err)
	}
}

// memConfigStore is the config persistence the Manager writes through, in memory.
//
// It takes its own lock, exactly as the real one does by virtue of being a database: the property under
// test is whether the COORDINATOR serializes its read-modify-write, and a store that were itself racy
// would make the test fail for the wrong reason.
type memConfigStore struct {
	mu        sync.Mutex
	overrides map[string]any
}

func (m *memConfigStore) GetConfig(context.Context) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]any, len(m.overrides))
	for k, v := range m.overrides {
		out[k] = v
	}
	return out, nil
}

func (m *memConfigStore) SetConfig(_ context.Context, overrides map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overrides = overrides
	return nil
}

func newTestCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	store := &memConfigStore{overrides: map[string]any{}}
	mgr := config.NewManager(store, config.Defaults())
	if err := mgr.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return &Coordinator{cfgMgr: mgr}
}

// Concurrent writes to different regions must all survive.
//
// config.Manager merges at the top-level key only, so each of these rewrites the whole `collector`
// subtree. Without serializing the read-modify-write, two mutations that overlap both start from the
// tree as it was before either wrote, and the one that finishes second erases the other's region — while
// that other mutation has already returned success to its caller.
func TestConcurrentRegionWritesAllSurvive(t *testing.T) {
	c := newTestCoordinator(t)
	ctx := context.Background()

	regions := []string{
		"eu-west-1", "eu-west-2", "eu-central-1", "us-east-1", "us-east-2",
		"us-west-1", "us-west-2", "ap-south-1", "ap-northeast-1", "sa-east-1",
	}

	var wg sync.WaitGroup
	errs := make([]error, len(regions))
	for i, region := range regions {
		wg.Add(1)
		go func(i int, region string) {
			defer wg.Done()
			enabled := true
			errs[i] = c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
				return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &enabled})
			})
		}(i, region)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writing %s: %v", regions[i], err)
		}
	}

	tree := c.storedCollectorSettingsTree()
	for _, region := range regions {
		if !collectorsettings.HasRegion(tree, region) {
			t.Errorf("region %s was lost; survivors: %v", region, collectorsettings.ConfiguredRegions(tree))
		}
	}
}

func TestConfiguredCollectorsRemainVisibleBeforeProviderRecord(t *testing.T) {
	now := time.Unix(100, 0)
	settings := collectorsettings.Read(map[string]any{collectorsettings.Key: map[string]any{
		collectorsettings.KeyEnabled: true,
		collectorsettings.KeyRegions: map[string]any{
			"eu-west-1": map[string]any{collectorsettings.KeyEnabled: true, collectorsettings.KeyHostNetworkID: "vpc-new"},
			"us-east-1": map[string]any{collectorsettings.KeyEnabled: true},
		},
	}})
	recorded := []collectorreport.ModelInput{{Region: "us-east-1", HostNetworkID: "vpc-recorded"}}

	got := includeConfiguredCollectorReports(recorded, []string{"eu-west-1", "us-east-1"}, settings, now)
	if regions := []string{got[0].Region, got[1].Region}; !reflect.DeepEqual(regions, []string{"eu-west-1", "us-east-1"}) {
		t.Fatalf("regions = %v", regions)
	}
	if got[0].HostNetworkID != "vpc-new" || got[0].Desired.HostNetworkID != "vpc-new" || !got[0].UpdatedAt.Equal(now) {
		t.Fatalf("configured-only report = %+v", got[0])
	}
	if got[1].HostNetworkID != "vpc-recorded" {
		t.Fatalf("recorded report was replaced by desired-only state: %+v", got[1])
	}
}

func TestRecordedConvergenceFailureBecomesStructuredIssue(t *testing.T) {
	providerMessage := "Collector convergence failed: converging shared identity: AccessDenied: iam:GetRole"
	recorded, problems := recordedCollectorProblems(collectorstatus.StatusError, providerMessage)

	if recorded != "" {
		t.Fatalf("recorded message = %q, want it consumed so the report does not duplicate it", recorded)
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one structured convergence failure", problems)
	}
	if problems[0].Code != "CollectorConvergenceFailed" ||
		problems[0].Severity != collectorstatus.SeverityError || problems[0].Message != providerMessage {
		t.Fatalf("problem = %+v", problems[0])
	}

	// Unrelated recorded diagnoses keep their existing prose channel; they must not be mislabeled as
	// provider convergence failures merely because their status is Error.
	recorded, problems = recordedCollectorProblems(collectorstatus.StatusError, "the Collector rejected its configuration")
	if recorded == "" || len(problems) != 0 {
		t.Fatalf("unrelated diagnosis was reclassified: recorded=%q problems=%v", recorded, problems)
	}
}

// Writing a region must not freeze the code defaults into the database.
//
// The stored override replaces the whole `collector` key, so persisting the EFFECTIVE tree would write
// today's default cpu/memory/scaling in alongside the region — and a later release raising a default
// would then never reach this connector, with nothing to say why.
func TestWritingARegionDoesNotPersistTheCodeDefaults(t *testing.T) {
	c := newTestCoordinator(t)
	enabled := true

	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, "eu-west-1", collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	stored := c.storedCollectorSettingsTree()
	if _, frozen := stored[collectorsettings.KeyDefaults]; frozen {
		t.Errorf("the defaults subtree was persisted into the override: %v", stored)
	}

	// And the defaults still reach a reader, from the code rather than from the database.
	region := collectorsettings.Read(c.cfgMgr.Effective()).Region("eu-west-1")
	if region.CPU != collectorsettings.DefaultCPU || region.Memory != collectorsettings.DefaultMemory {
		t.Errorf("defaults did not resolve: cpu=%q memory=%q", region.CPU, region.Memory)
	}
}

// A failed mutation must leave the tree as it was, rather than half-written.
func TestFailedMutationWritesNothing(t *testing.T) {
	c := newTestCoordinator(t)
	ctx := context.Background()
	enabled := true

	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, "eu-west-1", collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	cpu := "not-a-number"
	if err := c.mutateCollectorSettings(ctx, func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, "us-east-1", collectorsettings.Patch{CPU: &cpu})
	}); err == nil {
		t.Fatal("an invalid patch was accepted")
	}

	tree := c.storedCollectorSettingsTree()
	if collectorsettings.HasRegion(tree, "us-east-1") {
		t.Error("the refused region was written anyway")
	}
	if !collectorsettings.HasRegion(tree, "eu-west-1") {
		t.Error("the refused write damaged the region that was already there")
	}
}
