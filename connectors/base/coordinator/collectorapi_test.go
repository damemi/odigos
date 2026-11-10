package coordinator

import (
	"context"
	"reflect"
	"strings"
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
	details map[string]string
	updated map[string]time.Time
	records map[string]store.CollectorRecord
}

func TestCollectorRoutingReportWithoutDirectFallback(t *testing.T) {
	routing := collectorRoutingReportContext{markedRegions: map[string]bool{"eu-west-1": true}}
	ready := time.Now()
	in := collectorreport.ModelInput{
		Region: "eu-west-1", Status: collectorstatus.StatusReady, Mode: spec.ModeAutomatic,
		OpAMPLastSeenAt: &ready, HeartbeatInterval: time.Minute, Now: ready, FirstReadyAt: &ready,
	}
	routing.annotate(&in)
	if len(in.ExtraIssues) != 0 {
		t.Fatalf("optional global fallback reported as a problem: %+v", in.ExtraIssues)
	}
	if report := collectorreport.ToModel(in); report.Status != string(collectorstatus.StatusReady) || len(in.Problems) != 0 ||
		strings.Contains(report.Message, "global direct OTLP destination") {
		t.Fatalf("workload fallback changed Collector health: status=%s message=%q problems=%+v", report.Status, report.Message, in.Problems)
	}
	routing.progress = map[string]api.CollectorRoutingProgress{"eu-west-1": {Instrumented: 1, Routed: 1}}
	in.ExtraIssues = nil
	routing.annotate(&in)
	if len(in.ExtraIssues) != 1 || in.ExtraIssues[0].Code != "CollectorRolloutComplete" {
		t.Fatalf("Collector-only routing progress = %+v", in.ExtraIssues)
	}
	routing.progress = nil
	routing.directFound = true
	in.ExtraIssues, in.FirstReadyAt = nil, nil
	routing.annotate(&in)
	if len(in.ExtraIssues) != 1 || in.ExtraIssues[0].Code != "CollectorCutoverAwaitingReadiness" {
		t.Fatalf("missing first-ready issue: %+v", in.ExtraIssues)
	}
	in.FirstReadyAt, in.ExtraIssues, in.Now = &ready, nil, ready
	routing.annotate(&in)
	if len(in.ExtraIssues) != 1 || in.ExtraIssues[0].Code != "CollectorCutoverAwaitingWorkload" {
		t.Fatalf("ready Collector without eligible pass has wrong issue: %+v", in.ExtraIssues)
	}
	routing.announcements = map[string]*store.CutoverAnnouncement{"eu-west-1": {
		FirstReadyAt: ready, AnnouncedAt: ready, Announcing: true,
	}}
	in.ExtraIssues = nil
	routing.annotate(&in)
	if len(in.ExtraIssues) != 1 || in.ExtraIssues[0].Code != "CollectorRolloutStarting" {
		t.Fatalf("ready Collector has no announcement: %+v", in.ExtraIssues)
	}
	in.ExtraIssues, in.Now = nil, ready.Add(time.Minute)
	routing.announcements["eu-west-1"].Announcing = false
	routing.annotate(&in)
	if len(in.ExtraIssues) != 0 {
		t.Fatalf("announcement did not expire: %+v", in.ExtraIssues)
	}
	routing.progress = map[string]api.CollectorRoutingProgress{"eu-west-1": {
		Instrumented: 3, Routed: 1, Failed: 1, FailureRefs: []string{"cluster/failing"},
	}}
	routing.annotate(&in)
	if len(in.ExtraIssues) != 2 || in.ExtraIssues[0].Code != "CollectorRolloutInProgress" || in.ExtraIssues[1].Code != "CollectorRolloutWorkloadFailed" || in.ExtraIssues[1].Severity != "warning" {
		t.Fatalf("rollout progress and failure were not reported: %+v", in.ExtraIssues)
	}
	if report := collectorreport.ToModel(in); report.Status != string(collectorstatus.StatusReady) || len(in.Problems) != 0 {
		t.Fatalf("workload cutover failure changed Collector health: status=%s problems=%+v", report.Status, in.Problems)
	}
}

func TestCollectorTeardownReportHasGracePeriodAndKeepsRetrying(t *testing.T) {
	const region = "us-east-1"
	const kind = "aws.fargate-collector"
	ctx := context.Background()
	c := newTestCoordinator(t)
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	startedAt := time.Now().Add(-10 * time.Minute)
	resources.states[region] = store.CollectorResourcesTearingDown
	resources.updated[region] = startedAt
	resources.records[region] = store.CollectorRecord{
		Key: store.CollectorKey{Region: region}, Kind: kind, Mode: spec.ModeAutomatic,
		Origin: "odigos", NetworkID: "vpc-old", WorkloadRef: "service-old",
	}
	deployer := &alwaysRetryTeardownDeployer{recorder: newRecorder()}
	c.provider = collectorProvider{deployers: map[string]api.CollectorDeployer{kind: deployer}}
	oldHeartbeat := startedAt.Add(-time.Minute)
	input := collectorreport.ModelInput{
		Region: region, Mode: spec.ModeAutomatic, Origin: "odigos", Status: collectorstatus.StatusError,
		RecordedMessage: "old AWS DependencyViolation", OpAMPLastSeenAt: &oldHeartbeat,
		HeartbeatInterval: time.Minute, Now: time.Now(),
	}
	if err := c.addCollectorResourceStateProblem(ctx, &input, store.CollectorResourcesTearingDown); err != nil {
		t.Fatal(err)
	}
	if report := collectorreport.ToModel(input); report.Status != string(collectorstatus.StatusDeleting) ||
		strings.Contains(report.Message, "old AWS") || strings.Contains(report.Message, "OpAMP") ||
		len(report.Issues) != 1 || report.Issues[0].Code != "CollectorTeardownInProgress" {
		t.Fatalf("retryable teardown should show only Deleting: %+v", report)
	}

	resources.updated[region] = time.Now().Add(-collectorTeardownGracePeriod - time.Minute)
	input.Now = time.Now()
	if err := c.addCollectorResourceStateProblem(ctx, &input, store.CollectorResourcesTearingDown); err != nil {
		t.Fatal(err)
	}
	if report := collectorreport.ToModel(input); report.Status != string(collectorstatus.StatusError) ||
		len(report.Issues) != 1 || report.Issues[0].Code != "CollectorTeardownTimedOut" ||
		strings.Contains(report.Message, "old AWS") {
		t.Fatalf("stalled teardown should show its timeout: %+v", report)
	}
	c.retryCollectorTeardowns(ctx)
	if deployer.attempts != 1 || resources.states[region] != store.CollectorResourcesTearingDown {
		t.Fatalf("timeout stopped retry: attempts=%d state=%s", deployer.attempts, resources.states[region])
	}
}

type alwaysRetryTeardownDeployer struct {
	*recorder
	attempts int
}

func (d *alwaysRetryTeardownDeployer) TeardownCollector(context.Context, api.CollectorNetwork) error {
	d.attempts++
	return api.ErrCollectorTeardownRetry
}

func TestAutomaticNetworkMovePersistsIntentBeforeRemovingOldStack(t *testing.T) {
	const region = "eu-west-1"
	c := collectorCoordinator(t, automaticCaps(), map[string]any{
		collectorsettings.KeyRegions: map[string]any{region: map[string]any{
			collectorsettings.KeyEnabled: true, collectorsettings.KeyHostNetworkID: "vpc-old",
		}},
	}, newRecorder())
	resources := c.collectorResources.(*memoryCollectorResourceStore)
	resources.states[region] = store.CollectorResourcesCreated
	resources.records[region] = store.CollectorRecord{
		Key: store.CollectorKey{Region: region}, Mode: spec.ModeAutomatic, Origin: "odigos",
		NetworkID: "vpc-old", WorkloadRef: "service-old",
	}
	network := "vpc-new"
	if err := c.UpdateManagedCollector(context.Background(), region, collectorsettings.Patch{HostNetworkID: &network}); err != nil {
		t.Fatal(err)
	}
	if got := resources.states[region]; got != store.CollectorResourcesMoving || resources.details[region] != network {
		t.Fatalf("migration intent was not persisted: state=%s detail=%q", got, resources.details[region])
	}
	if got := c.CollectorSettings().Region(region).HostNetworkID; got != "vpc-old" {
		t.Fatalf("changed desired VPC before old stack teardown: %s", got)
	}
	delete(resources.records, region) // provider teardown removed the old row
	c.finishCollectorMigration(context.Background(), region, network)
	if got := c.CollectorSettings().Region(region).HostNetworkID; got != network {
		t.Fatalf("target VPC was not installed after teardown: %s", got)
	}
	if got := resources.states[region]; got != store.CollectorResourcesNotCreated {
		t.Fatalf("new stack cannot provision: state=%s", got)
	}
}

type retryingMigrationDeployer struct {
	*recorder
	resources *memoryCollectorResourceStore
	attempts  int
}

type firstPassTeardownDeployer struct {
	*recorder
	resources *memoryCollectorResourceStore
}

func (d *firstPassTeardownDeployer) TeardownCollector(_ context.Context, network api.CollectorNetwork) error {
	d.tornDown = append(d.tornDown, network.NetworkID)
	return d.resources.DeleteManagedCollector(context.Background(), store.CollectorKey{Region: network.Region})
}

func TestAutomaticFirstPassRowUsesProviderTeardownWithoutOpaqueRefs(t *testing.T) {
	const region = "us-east-1"
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	deployer := &firstPassTeardownDeployer{recorder: newRecorder(), resources: resources}
	c.provider = collectorProvider{deployers: map[string]api.CollectorDeployer{collectorKind: deployer}}
	enabled := true
	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatal(err)
	}
	resources.states[region] = store.CollectorResourcesProvisioning
	record := store.CollectorRecord{
		Key: store.CollectorKey{Region: region}, NetworkID: "vpc-first", Kind: collectorKind,
		Mode: spec.ModeAutomatic, Origin: "odigos",
	}
	resources.records[region] = record
	if c.canDiscardCollectorState(region, &record, store.CollectorResourcesProvisioning, true) {
		t.Fatal("first-pass row was eligible for local-only removal")
	}
	result, err := c.TeardownManagedCollector(context.Background(), region)
	if err != nil || !result.Deleted || !reflect.DeepEqual(deployer.tornDown, []string{"vpc-first"}) {
		t.Fatalf("first-pass teardown = %+v, %v, provider calls=%v", result, err, deployer.tornDown)
	}
}

func (d *retryingMigrationDeployer) TeardownCollector(_ context.Context, network api.CollectorNetwork) error {
	d.attempts++
	if d.attempts == 1 {
		return api.ErrCollectorTeardownRetry
	}
	return d.resources.DeleteManagedCollector(context.Background(), store.CollectorKey{Region: network.Region})
}

func TestAutomaticNetworkMoveRetriesUntilOldStackIsGone(t *testing.T) {
	const region = "eu-west-1"
	c := newTestCoordinator(t)
	c.cfg.Capabilities = automaticCaps()
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	deployer := &retryingMigrationDeployer{recorder: newRecorder(), resources: resources}
	c.provider = collectorProvider{deployers: map[string]api.CollectorDeployer{collectorKind: deployer}}
	oldNetwork, newNetwork := "vpc-old", "vpc-new"
	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{HostNetworkID: &oldNetwork})
	}); err != nil {
		t.Fatal(err)
	}
	resources.states[region], resources.details[region] = store.CollectorResourcesMoving, newNetwork
	resources.records[region] = store.CollectorRecord{Key: store.CollectorKey{Region: region}, Kind: collectorKind,
		Mode: spec.ModeAutomatic, Origin: "odigos", NetworkID: oldNetwork, WorkloadRef: "service-old"}
	c.retryCollectorTeardowns(context.Background())
	if resources.states[region] != store.CollectorResourcesMoving || c.CollectorSettings().Region(region).HostNetworkID != oldNetwork {
		t.Fatalf("migration moved before AWS release: state=%s", resources.states[region])
	}
	c.retryCollectorTeardowns(context.Background())
	if resources.states[region] != store.CollectorResourcesNotCreated || c.CollectorSettings().Region(region).HostNetworkID != newNetwork {
		t.Fatalf("migration did not reach target: state=%s network=%s", resources.states[region], c.CollectorSettings().Region(region).HostNetworkID)
	}
}

func newMemoryCollectorResourceStore() *memoryCollectorResourceStore {
	return &memoryCollectorResourceStore{states: map[string]store.CollectorResourceState{}, details: map[string]string{}, updated: map[string]time.Time{}, records: map[string]store.CollectorRecord{}}
}

func (m *memoryCollectorResourceStore) CollectorResourceStateUpdatedAt(_ context.Context, region string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updated[region], nil
}

func (m *memoryCollectorResourceStore) CollectorResourceStateDetail(_ context.Context, region string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.details[region], nil
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

func (m *memoryCollectorResourceStore) SetCollectorResourceState(_ context.Context, region string, state store.CollectorResourceState, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[region] = state
	m.details[region] = detail
	m.updated[region] = time.Now()
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

func (m *memoryCollectorResourceStore) UpsertManagedCollector(_ context.Context, key store.CollectorKey, networkID, kind, mode, origin string, adoptedAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.records[key.Region]
	rec.Key, rec.NetworkID, rec.Kind, rec.Mode = key, networkID, kind, mode
	if rec.Origin == "" {
		rec.Origin = origin
	}
	if rec.AdoptedAt == nil {
		rec.AdoptedAt = adoptedAt
	}
	m.records[key.Region] = rec
	return nil
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

func (m *memoryCollectorResourceStore) DeleteCollectorActionsForRegion(context.Context, string) error {
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

func TestManualCollectorTeardownRetainsInfrastructure(t *testing.T) {
	c := newTestCoordinator(t)
	c.cfg.Capabilities = spec.Capabilities{Collectors: []spec.CollectorCapability{{Type: "test.collector", Management: true}}}
	c.provider = collectorProvider{}
	resources := newMemoryCollectorResourceStore()
	c.collectorResources = resources
	region := "eu-west-1"
	resources.records[region] = store.CollectorRecord{
		Key: store.CollectorKey{Region: region}, Mode: spec.ModeManual, Origin: "customer",
		WorkloadRef: "arn:customer:service", FrontendRef: "arn:customer:nlb",
	}
	enabled := true
	if err := c.mutateCollectorSettings(context.Background(), func(tree map[string]any) (map[string]any, error) {
		return collectorsettings.WriteRegion(tree, region, collectorsettings.Patch{Enabled: &enabled})
	}); err != nil {
		t.Fatal(err)
	}
	got, err := c.TeardownManagedCollector(context.Background(), region)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deleted || len(got.Retained) != 2 {
		t.Fatalf("manual teardown = %+v, want declaration removed and two retained refs", got)
	}
	if _, err := resources.GetManagedCollector(context.Background(), store.CollectorKey{Region: region}); err != store.ErrCollectorNotFound {
		t.Fatalf("manual row still present: %v", err)
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
