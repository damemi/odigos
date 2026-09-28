package collectorsettings

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
)

func TestDefaultsAreDisabledAndRegional(t *testing.T) {
	tree := Defaults()[Key].(map[string]any)
	if tree[KeyEnabled] != false {
		t.Fatalf("enabled = %v, want false", tree[KeyEnabled])
	}
	if _, ok := tree[KeyRegions].(map[string]any); !ok {
		t.Fatalf("regions = %T, want map", tree[KeyRegions])
	}
	if _, legacy := tree["networks"]; legacy {
		t.Fatal("defaults still expose the retired per-network Collector tree")
	}
}

func TestRegionInheritsDefaultsAndAppliesOverrides(t *testing.T) {
	s := Read(map[string]any{Key: map[string]any{
		KeyEnabled: true,
		KeyDefaults: map[string]any{
			KeyCPU: "2048", KeyMemory: "4096", KeyArch: "arm64",
			KeyIngress:  map[string]any{KeyAllowNetworkCIDRFallback: false},
			KeyProvider: map[string]any{"placement": map[string]any{"class": "private", "zones": []any{"a"}}},
		},
		KeyRegions: map[string]any{
			"eu-west-1": map[string]any{
				KeyHostNetworkID: "network-host", KeyCPU: "1024",
				KeyProvider: map[string]any{"placement": map[string]any{"zones": []any{"b"}}},
				KeyTLS:      map[string]any{KeyTLSMode: "loadBalancer", KeyTLSHostname: "collector.example.com"},
			},
		},
	}})

	eu := s.Region("eu-west-1")
	if eu.Region != "eu-west-1" || eu.HostNetworkID != "network-host" || eu.CPU != "1024" || eu.Memory != "4096" || eu.Arch != "arm64" {
		t.Fatalf("eu-west-1 = %+v", eu)
	}
	wantProvider := map[string]any{"placement": map[string]any{"class": "private", "zones": []any{"b"}}}
	if !reflect.DeepEqual(eu.Provider, wantProvider) {
		t.Fatalf("provider settings = %#v, want %#v", eu.Provider, wantProvider)
	}
	if eu.TLS.Mode != collectoringress.TLSLoadBalancer || eu.TLS.Hostname != "collector.example.com" {
		t.Fatalf("TLS = %+v", eu.TLS)
	}
	if eu.Ingress.AllowNetworkCIDRFallback {
		t.Fatal("regional ingress did not inherit the explicit default")
	}

	unconfigured := s.Region("us-east-1")
	if !unconfigured.Enabled || unconfigured.Region != "us-east-1" || unconfigured.CPU != "2048" || unconfigured.HostNetworkID != "" {
		t.Fatalf("unconfigured region = %+v", unconfigured)
	}
}

func TestRegionalEnablementAndStableDeployRegions(t *testing.T) {
	s := Read(map[string]any{Key: map[string]any{
		KeyEnabled: true,
		KeyRegions: map[string]any{
			"us-east-1":  map[string]any{},
			"ap-south-1": map[string]any{},
			"eu-west-1":  map[string]any{KeyEnabled: false},
		},
	}})
	if got := s.DeployRegions(); !reflect.DeepEqual(got, []string{"ap-south-1", "us-east-1"}) {
		t.Fatalf("DeployRegions = %v", got)
	}
	if s.Region("eu-west-1").Deploys() {
		t.Fatal("disabled region is deployable")
	}
}

func TestFeatureSwitchOverridesRegionalEntries(t *testing.T) {
	s := Read(map[string]any{Key: map[string]any{
		KeyEnabled: false,
		KeyRegions: map[string]any{"eu-west-1": map[string]any{KeyEnabled: true}},
	}})
	if s.Region("eu-west-1").Deploys() || len(s.DeployRegions()) != 0 {
		t.Fatalf("disabled feature produced deployable settings: %+v", s)
	}
}

func TestLegacyNetworkTreeCannotCreateCollectorIdentity(t *testing.T) {
	s := Read(map[string]any{Key: map[string]any{
		KeyEnabled: true,
		"networks": map[string]any{
			"vpc-old": map[string]any{"sharedWith": "vpc-hub", KeyCPU: "8192", KeyEnabled: false},
		},
	}})
	if len(s.Regions) != 0 || s.Region("eu-west-1").CPU == "8192" || !s.Region("eu-west-1").Enabled {
		t.Fatalf("legacy network tree affected regional settings: %+v", s)
	}
}

func TestMemoryMiB(t *testing.T) {
	if got, err := (Network{Memory: "4096"}).MemoryMiB(); err != nil || got != 4096 {
		t.Fatalf("MemoryMiB = %d, %v", got, err)
	}
	if _, err := (Network{Memory: "large"}).MemoryMiB(); err == nil {
		t.Fatal("invalid memory was accepted")
	}
}
