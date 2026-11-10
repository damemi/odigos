package services

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/snapshotsummary"
	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
)

func TestCloudConnectorCollectorModes(t *testing.T) {
	got := cloudConnectorCollectors([]cloudconnectorsv1.CloudConnectorCollectorCapability{
		{Type: "manual", Management: true},
		{Type: "automatic", Management: true, Deployment: true},
	})
	if len(got) != 2 || got[0].Mode != "manual" || got[1].Mode != "automatic" {
		t.Fatalf("collector capabilities = %+v", got)
	}
	if got[0].Deployment || !got[1].Deployment {
		t.Fatalf("deployment flags lost: %+v", got)
	}
}

type fakeProxyMetaReader struct {
	values redisclient.RedisHashFieldValuesByKey
	err    error
}

func (f fakeProxyMetaReader) GetProxyMetaKeyValues(context.Context) (redisclient.RedisHashFieldValuesByKey, error) {
	return f.values, f.err
}

func TestExtractProxyIDFromProxyMetaKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "current two-part key format",
			key:  "proxy-meta:proxy-1",
			want: "proxy-1",
		},
		{
			name: "legacy multi-part key format",
			key:  "proxy-meta:backend-a:proxy-2",
			want: "proxy-2",
		},
		{
			name: "single part key is invalid",
			key:  "proxy-meta",
			want: "",
		},
		{
			name: "empty proxy id is invalid",
			key:  "proxy-meta:",
			want: "",
		},
		{
			name: "empty string key is invalid",
			key:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractProxyIDFromProxyMetaKey(tt.key)
			if got != tt.want {
				t.Fatalf("extractProxyIDFromProxyMetaKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestGetAllProxyMetadata_ParsesTwoPartKeysAndSkipsMalformedKeys(t *testing.T) {
	ctx := context.Background()

	svc := &ComputePlatformService{
		redis: fakeProxyMetaReader{
			values: redisclient.RedisHashFieldValuesByKey{
				"proxy-meta:proxy-two-part": {
					consts.ProxyMetaFieldPlatformName:  "platform-a",
					consts.ProxyMetaFieldPlatformType:  string(consts.PlatformTypeVM),
					consts.ProxyMetaFieldOdigosVersion: "1.2.3",
					consts.ProxyMetaFieldConnectedAt:   "1712345",
					consts.ProxyMetaFieldLastSeenAt:    "invalid-timestamp",
					consts.ProxyMetaFieldStatus:        string(consts.StatusConnected),
				},
				"proxy-meta:backend-a:proxy-three-part": {
					consts.ProxyMetaFieldPlatformName: "platform-b",
					consts.ProxyMetaFieldPlatformType: string(consts.PlatformTypeK8s),
					consts.ProxyMetaFieldStatus:       string(consts.StatusDisconnected),
				},
				"proxy-meta:": {
					consts.ProxyMetaFieldPlatformName: "malformed",
				},
				"proxy-meta": {
					consts.ProxyMetaFieldPlatformName: "also-malformed",
				},
			},
		},
	}

	metaByProxyID, err := svc.GetAllProxyMetadata(ctx)
	if err != nil {
		t.Fatalf("GetAllProxyMetadata returned error: %v", err)
	}

	if len(metaByProxyID) != 2 {
		t.Fatalf("expected 2 proxy entries, got %d", len(metaByProxyID))
	}

	twoPartMeta, ok := metaByProxyID["proxy-two-part"]
	if !ok {
		t.Fatalf("expected two-part key proxy metadata to be present")
	}
	if twoPartMeta.PlatformName != "platform-a" {
		t.Fatalf("expected platform name platform-a, got %q", twoPartMeta.PlatformName)
	}
	if twoPartMeta.PlatformType != consts.PlatformTypeVM {
		t.Fatalf("expected platform type %q, got %q", consts.PlatformTypeVM, twoPartMeta.PlatformType)
	}
	if twoPartMeta.ConnectedAt != 1712345 {
		t.Fatalf("expected connected_at 1712345, got %d", twoPartMeta.ConnectedAt)
	}
	if twoPartMeta.LastSeenAt != 0 {
		t.Fatalf("expected invalid last_seen_at to remain 0, got %d", twoPartMeta.LastSeenAt)
	}

	if _, ok := metaByProxyID["proxy-three-part"]; !ok {
		t.Fatalf("expected legacy three-part key proxy metadata to be present")
	}
	if _, ok := metaByProxyID[""]; ok {
		t.Fatalf("did not expect metadata entry with empty proxy id")
	}
}

func TestGetAllProxyMetadata_PropagatesReaderErrors(t *testing.T) {
	ctx := context.Background()
	expectedErr := context.DeadlineExceeded

	svc := &ComputePlatformService{
		redis: fakeProxyMetaReader{
			err: expectedErr,
		},
	}

	metaByProxyID, err := svc.GetAllProxyMetadata(ctx)
	if err == nil {
		t.Fatalf("expected GetAllProxyMetadata to return an error")
	}
	if err != expectedErr {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if metaByProxyID != nil {
		t.Fatalf("expected nil metadata map when reader returns error")
	}
}

func TestApplyProxyMetaToComputePlatform_PopulatesUnknownValues(t *testing.T) {
	cp := &model.ComputePlatform{
		ID:            "proxy-1",
		Name:          consts.UnknownValue,
		OdigosVersion: consts.UnknownValue,
		Type:          string(consts.PlatformTypeK8s),
		Status:        string(consts.StatusConnected),
		Users:         []*model.User{},
		Teams:         []*model.Team{},
	}
	meta := &redisclient.ProxyMetadata{
		PlatformName:         "production-cluster",
		PlatformType:         consts.PlatformTypeVM,
		OdigosVersion:        "v1.2.3",
		GraphQLSchemaVersion: "gql-1",
		ConnectedAt:          10,
		LastSeenAt:           25,
	}

	applyProxyMetaToComputePlatform(cp, meta)

	if cp.Name != "production-cluster" {
		t.Errorf("expected name to be populated from metadata, got %q", cp.Name)
	}
	if cp.OdigosVersion != "v1.2.3" {
		t.Errorf("expected odigos version to be populated from metadata, got %q", cp.OdigosVersion)
	}
	if cp.GraphqlSchemaVersion == nil || *cp.GraphqlSchemaVersion != "gql-1" {
		t.Errorf("expected graphql schema version to be populated from metadata, got %+v", cp.GraphqlSchemaVersion)
	}
	if cp.Type != string(consts.PlatformTypeVM) {
		t.Errorf("expected platform type vm, got %q", cp.Type)
	}
	if cp.ConnectedAt == nil || *cp.ConnectedAt != 10000 {
		t.Fatalf("expected connectedAt to be set to 10000ms, got %+v", cp.ConnectedAt)
	}
	if cp.LastSeenAt == nil || *cp.LastSeenAt != 25000 {
		t.Fatalf("expected lastSeenAt to be set to 25000ms, got %+v", cp.LastSeenAt)
	}
}

func TestApplyProxyMetaToComputePlatform_PreservesKnownValuesAndSkipsZeroTimestamps(t *testing.T) {
	existingSchema := "existing-schema"
	cp := &model.ComputePlatform{
		ID:                   "proxy-2",
		Name:                 "existing-name",
		OdigosVersion:        "v9.9.9",
		GraphqlSchemaVersion: &existingSchema,
		Type:                 string(consts.PlatformTypeVM),
		Status:               string(consts.StatusConnected),
		Users:                []*model.User{},
		Teams:                []*model.Team{},
	}
	meta := &redisclient.ProxyMetadata{
		PlatformName:         "new-name",
		PlatformType:         consts.PlatformTypeK8s,
		OdigosVersion:        "v0.0.1",
		GraphQLSchemaVersion: "new-schema",
		ConnectedAt:          0,
		LastSeenAt:           0,
	}

	applyProxyMetaToComputePlatform(cp, meta)

	if cp.Name != "existing-name" {
		t.Errorf("expected name to remain unchanged, got %q", cp.Name)
	}
	if cp.OdigosVersion != "v9.9.9" {
		t.Errorf("expected odigos version to remain unchanged, got %q", cp.OdigosVersion)
	}
	if cp.GraphqlSchemaVersion == nil || *cp.GraphqlSchemaVersion != "existing-schema" {
		t.Errorf("expected graphql schema version to remain unchanged, got %+v", cp.GraphqlSchemaVersion)
	}
	if cp.Type != string(consts.PlatformTypeK8s) {
		t.Errorf("expected platform type to be updated to k8s, got %q", cp.Type)
	}
	if cp.ConnectedAt != nil {
		t.Errorf("expected connectedAt to remain nil for zero timestamp, got %+v", cp.ConnectedAt)
	}
	if cp.LastSeenAt != nil {
		t.Errorf("expected lastSeenAt to remain nil for zero timestamp, got %+v", cp.LastSeenAt)
	}
}

type fakeSnapshotSummaryReader struct {
	summaries map[string]*snapshotsummary.Summary
	err       error
	gotIDs    []string
}

func (f *fakeSnapshotSummaryReader) GetClusterSnapshotSummaries(_ context.Context, clusterIDs []string) (map[string]*snapshotsummary.Summary, error) {
	f.gotIDs = clusterIDs
	return f.summaries, f.err
}

func TestAppendSnapshotStats_AttachesStatsToPlatformsWithSnapshots(t *testing.T) {
	cpMap := map[string]*model.ComputePlatform{
		"k8s-1": {ID: "k8s-1"},
		"vm-1":  {ID: "vm-1"},
	}
	reader := &fakeSnapshotSummaryReader{summaries: map[string]*snapshotsummary.Summary{
		"k8s-1": {
			Workloads:                  10,
			InstrumentedWorkloads:      6,
			WorkloadsByHealth:          map[string]int{"Success": 2, "Error": 1, "Failure": 1, "Pending": 1, "Waiting": 1},
			Namespaces:                 4,
			AutoInstrumentedNamespaces: 1,
			SnapshotTimestamp:          100,
		},
		"deleted": {Workloads: 1},
	}}

	appendSnapshotStats(context.Background(), cpMap, reader)

	sort.Strings(reader.gotIDs)
	if len(reader.gotIDs) != 2 || reader.gotIDs[0] != "k8s-1" || reader.gotIDs[1] != "vm-1" {
		t.Fatalf("expected summaries to be requested for every platform, got %v", reader.gotIDs)
	}
	want := &model.ComputePlatformStats{
		Workloads:                  10,
		InstrumentedWorkloads:      6,
		WorkloadsWithIssues:        2,
		WorkloadsInProgress:        2,
		Namespaces:                 4,
		AutoInstrumentedNamespaces: 1,
		UpdatedAt:                  100000,
	}
	if got := cpMap["k8s-1"].Stats; got == nil || *got != *want {
		t.Fatalf("unexpected stats:\n got: %+v\nwant: %+v", got, want)
	}
	if cpMap["vm-1"].Stats != nil {
		t.Fatalf("expected no stats for a platform without a snapshot, got %+v", cpMap["vm-1"].Stats)
	}
	if _, ok := cpMap["deleted"]; ok {
		t.Fatal("a summary without a matching platform must not add one")
	}
}

func TestAppendSnapshotStats_ReadErrorLeavesStatsEmpty(t *testing.T) {
	logger.InitLogger()
	cpMap := map[string]*model.ComputePlatform{"k8s-1": {ID: "k8s-1"}}
	reader := &fakeSnapshotSummaryReader{err: errors.New("redis down")}

	appendSnapshotStats(context.Background(), cpMap, reader)

	if cpMap["k8s-1"].Stats != nil {
		t.Fatalf("expected no stats after a failed read, got %+v", cpMap["k8s-1"].Stats)
	}
}

func TestAppendSnapshotStats_MixedPlatforms(t *testing.T) {
	cpMap := map[string]*model.ComputePlatform{
		"k8s":   {ID: "k8s", Type: "k8s"},
		"vm":    {ID: "vm", Type: "vm"},
		"other": {ID: "other", Type: "aws-ecs"},
	}
	reader := &fakeSnapshotSummaryReader{summaries: map[string]*snapshotsummary.Summary{
		"k8s":   {Workloads: 10, InstrumentedWorkloads: 6, Namespaces: 2, SnapshotTimestamp: 100},
		"vm":    {Workloads: 5, InstrumentedWorkloads: 3, WorkloadsByHealth: map[string]int{"Error": 1, "Waiting": 1}, SnapshotTimestamp: 200},
		"other": {Workloads: 2, InstrumentedWorkloads: 1, SnapshotTimestamp: 300},
	}}
	appendSnapshotStats(context.Background(), cpMap, reader)
	vm := cpMap["vm"].Stats
	if vm == nil || vm.Workloads != 5 || vm.InstrumentedWorkloads != 3 || vm.WorkloadsWithIssues != 1 || vm.WorkloadsInProgress != 1 || vm.Namespaces != 0 || vm.UpdatedAt != 200000 {
		t.Fatalf("unexpected VM statistics: %+v", vm)
	}
	if cpMap["k8s"].Stats == nil || cpMap["other"].Stats == nil || len(reader.gotIDs) != 3 {
		t.Fatal("expected one batched read covering all platform types")
	}
}
