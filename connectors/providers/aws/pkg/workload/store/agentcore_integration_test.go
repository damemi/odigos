package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
)

// Set this only to a disposable database with connector migrations applied.
func TestAgentCorePostgresInventoryAndSnapshots(t *testing.T) {
	dsn := os.Getenv("ODIGOS_AGENTCORE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ODIGOS_AGENTCORE_TEST_POSTGRES_DSN for PostgreSQL integration")
	}
	ctx := context.Background()
	name := fmt.Sprintf("agentcore-test-%d", time.Now().UnixNano())
	base, err := basestore.Open(ctx, dsn, name, "connector_aws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	s := New(base)
	t.Cleanup(func() {
		if err := s.PurgeConnectorInventory(ctx); err != nil {
			t.Error(err)
		}
	})
	r := agentcore.Runtime{ARN: "arn:aws:bedrock-agentcore:us-east-1:123:runtime/test", ID: "test", Name: "demo", Region: "us-east-1", Version: "2", Status: "READY", Language: "python", RuntimeVersion: "3.12", Origin: "connector", Distro: "python-agentcore-adot"}
	if err := s.UpsertAgentCore(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentCoreState(ctx, r.ARN, map[string]any{"conformance": "conformant", "instrumentation_phase": "instrumented", "settling": true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentCore(ctx, r); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListAgentCore(ctx)
	if err != nil || len(records) != 1 || records[0].LastSeen.IsZero() {
		t.Fatalf("list runtime: %v %v", records, err)
	}
	workloads, err := s.ListWorkloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(workloads) != 1 || !workloads[0].Instrumented || !workloads[0].Settling || workloads[0].Details.LatestReadyRevision != "2" || workloads[0].RuntimeVersion != "3.12" {
		t.Fatalf("report lost inventory/state: %+v", workloads)
	}
	arn, region, found, err := s.LookupDiscoveredWorkload(ctx, "demo", agentcore.Kind, "us-east-1")
	if err != nil || !found || arn != r.ARN || region != r.Region {
		t.Fatalf("lookup: %q %q %v %v", arn, region, found, err)
	}
	if err := s.SaveAgentCoreSnapshot(ctx, r.ARN, "snapshot", []byte(`{"previous":{"APP":"private"},"applied":{"OTEL_SERVICE_NAME":"demo"}}`)); err != nil {
		t.Fatal(err)
	}
	// A retry must not replace the rollback payload, and connector identities cannot read each other's snapshots.
	if err := s.SaveAgentCoreSnapshot(ctx, r.ARN, "snapshot", []byte(`{"replaced":true}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := s.LoadAgentCoreSnapshot(ctx, r.ARN, "snapshot")
	if err != nil || len(raw) < 40 {
		t.Fatalf("snapshot replaced or lost: %s %v", raw, err)
	}
	otherBase, err := basestore.Open(ctx, dsn, name+"-other", "connector_aws")
	if err != nil {
		t.Fatal(err)
	}
	defer otherBase.Close()
	if _, err := New(otherBase).LoadAgentCoreSnapshot(ctx, r.ARN, "snapshot"); err == nil {
		t.Fatal("snapshot crossed connector boundary")
	}
	r.Origin = ""
	if err := s.UpsertAgentCore(ctx, r); err != nil {
		t.Fatal(err)
	}
	state, err := s.AgentCoreState(ctx, r.ARN)
	if err != nil || state["settling"] != false || state["instrumentation_phase"] != "" || state["conformance"] != "conformant" {
		t.Fatalf("rollback completion state: %v %v", state, err)
	}
	if err := s.PruneAgentCore(ctx, r.Region, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	records, err = s.ListAgentCore(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("prune failed: %v %v", records, err)
	}
	if err := s.UpsertAgentCore(ctx, r); err != nil {
		t.Fatal(err)
	}
	records, err = s.ListAgentCore(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("rediscovery failed: %v %v", records, err)
	}
}
