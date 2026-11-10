package config

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connector-opamp-gateway/internal/gateway"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@db:5432/odigos_central")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// OpAMP's default path; the edge forwards this one route and nothing else.
	if c.OpAMPPath != "/v1/opamp" {
		t.Errorf("OpAMPPath = %q", c.OpAMPPath)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q", c.ListenAddr)
	}
	if c.RevalidateInterval != gateway.DefaultRevalidateInterval {
		t.Errorf("RevalidateInterval = %s", c.RevalidateInterval)
	}
}

// TestLoadRequiresADatabase: without the credential store the gateway can only answer 503, so starting
// without one would produce a component that looks healthy and refuses the entire fleet.
func TestLoadRequiresADatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Error("Load accepted an empty DATABASE_URL")
	}
}

// TestRevalidateIntervalCannotBeDisabled: a zero or negative interval reads as "turn it off", and off
// means a revoked credential keeps its open session — and its configuration pushes — until the
// Collector chooses to reconnect.
func TestRevalidateIntervalCannotBeDisabled(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://db/odigos_central")

	for _, raw := range []string{"0", "0s", "-1m", "not-a-duration"} {
		t.Setenv("ODIGOS_OPAMP_REVALIDATE_INTERVAL", raw)
		if _, err := Load(); err == nil {
			t.Errorf("Load accepted ODIGOS_OPAMP_REVALIDATE_INTERVAL=%q", raw)
		}
	}

	t.Setenv("ODIGOS_OPAMP_REVALIDATE_INTERVAL", "10s")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RevalidateInterval != 10*time.Second {
		t.Errorf("RevalidateInterval = %s, want 10s", c.RevalidateInterval)
	}
}

// TestOpAMPPathMustBeAbsolute: a relative path registers a pattern that never matches, so the gateway
// would start, report ready, and 404 the fleet.
func TestOpAMPPathMustBeAbsolute(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://db/odigos_central")
	t.Setenv("ODIGOS_OPAMP_PATH", "v1/opamp")
	if _, err := Load(); err == nil {
		t.Error("Load accepted a relative ODIGOS_OPAMP_PATH")
	}
}
