package spec

import "testing"

func TestBootstrapRegions(t *testing.T) {
	for _, tc := range []struct{ provider, defaultRegion string }{{"aws", "us-east-1"}, {"azure", "eastus"}, {"gcp", "us-central1"}, {"mulesoft", "us"}} {
		t.Run(tc.provider, func(t *testing.T) {
			cfg := MustGet(tc.provider).Connector.BootstrapRegion
			got, err := cfg.Resolve("")
			if err != nil || got != tc.defaultRegion {
				t.Fatalf("default = %q, %v", got, err)
			}
			seen := map[string]bool{}
			for _, region := range cfg.SupportedRegions {
				if seen[region] {
					t.Fatalf("duplicate region %q", region)
				}
				seen[region] = true
				if got, err := cfg.Resolve(region); err != nil || got != region {
					t.Fatalf("region = %q, %v", got, err)
				}
			}
			if _, err := cfg.Resolve("invalid-region"); err == nil {
				t.Fatal("invalid region accepted")
			}
			// MuleSoft's "region" is the Anypoint control plane (us, eu), which the connector does implement.
			if tc.provider != "aws" && tc.provider != "mulesoft" && len(cfg.SupportedRegions) != 1 {
				t.Fatal("unimplemented provider must use a fixed default")
			}
		})
	}
	if got, err := MustGet("aws").Connector.BootstrapRegion.Resolve("eu-west-1"); err != nil || got != "eu-west-1" {
		t.Fatalf("AWS selection = %q, %v", got, err)
	}
}
