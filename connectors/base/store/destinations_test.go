package store

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

func TestCollectorTargetsProjectFirstReadyLatch(t *testing.T) {
	readyAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	records := []CollectorRecord{
		{Key: CollectorKey{Region: "us-east-1"}, FirstReadyAt: nil},
		{Key: CollectorKey{Region: "eu-west-1"}, FirstReadyAt: &readyAt},
	}

	targets := collectorTargets(records)
	if len(targets) != len(records) {
		t.Fatalf("targets = %d, want %d", len(targets), len(records))
	}
	if targets[0].EverReady {
		t.Error("Collector without FirstReadyAt was marked EverReady")
	}
	if !targets[1].EverReady {
		t.Error("Collector with FirstReadyAt was not marked EverReady")
	}
}

func TestCollectorTargetsAreRegionalAndKeepHostNetworkAsMetadata(t *testing.T) {
	records := []CollectorRecord{
		{
			Key:          CollectorKey{Region: "eu-west-1"},
			NetworkID:    "vpc-host",
			EndpointGRPC: "collector.internal:4317",
			EndpointHTTP: "https://collector.internal:4318",
			TLSMode:      "loadBalancer",
			Status:       collectorstatus.StatusReady,
		},
	}

	targets := collectorTargets(records)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	got := targets[0]
	if got.Region != "eu-west-1" || got.Network != "vpc-host" {
		t.Fatalf("target identity/host = %+v", got)
	}
	if got.Endpoint != "collector.internal:4317" || got.HTTPEndpoint != "https://collector.internal:4318" {
		t.Fatalf("target endpoints = %+v", got)
	}
	if got.Insecure {
		t.Fatal("load-balancer TLS target marked insecure")
	}
}

func TestDestinationRecordToDomainPreservesRegion(t *testing.T) {
	got := DestinationRecordToDomain(DestinationRecord{
		Name: "regional", Type: "odigos-managed-collector", Region: "us-east-1",
		Signals: []string{"TRACES"},
	})
	if got.Region != "us-east-1" {
		t.Fatalf("region = %q, want us-east-1", got.Region)
	}
}
