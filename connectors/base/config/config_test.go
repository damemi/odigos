package config

import "testing"

// TestPlatformIDFromCR is a safety flag, not a convenience one.
//
// PlatformID falls back to ConnectorName so local runs and older controllers still have a routing id.
// That fallback is harmless for routing and dangerous for identity: a managed Collector's credential is
// bound to the CR UID at issue, so anything comparing live credentials against PlatformID to detect a
// recreated connector would, under the fallback, find that none match and conclude the entire fleet
// needs revoking. This flag is what lets that caller refuse to draw the conclusion.
func TestPlatformIDFromCR(t *testing.T) {
	t.Setenv("ODIGOS_CONNECTOR_NAME", "aws-prod")
	t.Setenv("DATABASE_URL", "postgres://db/odigos_central")

	t.Run("stamped by the controller", func(t *testing.T) {
		t.Setenv("ODIGOS_CONNECTOR_ID", "3f2b1c9e-0000-4a00-8000-000000000001")

		c, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if !c.PlatformIDFromCR {
			t.Error("PlatformIDFromCR is false for an explicitly stamped CR UID")
		}
		if c.PlatformID != "3f2b1c9e-0000-4a00-8000-000000000001" {
			t.Errorf("PlatformID = %q", c.PlatformID)
		}
	})

	t.Run("falling back to the connector name", func(t *testing.T) {
		t.Setenv("ODIGOS_CONNECTOR_ID", "")

		c, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if c.PlatformIDFromCR {
			t.Error("PlatformIDFromCR is true for the ConnectorName fallback, which is not a CR UID")
		}
		if c.PlatformID != "aws-prod" {
			t.Errorf("PlatformID = %q, want the connector name", c.PlatformID)
		}
	})
}

func TestBootstrapRegionFromEnv(t *testing.T) {
	t.Setenv("ODIGOS_CONNECTOR_NAME", "aws-prod")
	t.Setenv("DATABASE_URL", "postgres://db/odigos_central")
	for _, region := range []string{"", "eu-west-1", "invalid"} {
		t.Run(region, func(t *testing.T) {
			t.Setenv("ODIGOS_BOOTSTRAP_REGION", region)
			cfg, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ResolveBootstrapRegion("aws")
			if region == "invalid" {
				if err == nil {
					t.Fatal("invalid region accepted")
				}
				return
			}
			want := region
			if want == "" {
				want = "us-east-1"
			}
			if err != nil || cfg.BootstrapRegion != want {
				t.Fatalf("bootstrap region = %q, %v", cfg.BootstrapRegion, err)
			}
		})
	}
}
