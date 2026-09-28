package store

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

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
