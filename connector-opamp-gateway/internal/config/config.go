// Package config is the gateway's startup configuration. Everything here comes from the environment
// the Helm chart sets; nothing is read from a customer-editable surface.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/odigos-io/odigos-enterprise/connector-opamp-gateway/internal/gateway"
)

// Config is what the gateway needs to start.
type Config struct {
	// ListenAddr serves both the OpAMP endpoint and the probes. One listener: the probes have to be
	// reachable exactly when the OpAMP path is, and splitting them lets a gateway report Ready on a
	// port that is answering while the one that matters is not.
	ListenAddr string
	// OpAMPPath is the public path the edge forwards. OpAMP's default is /v1/opamp.
	OpAMPPath string
	// DatabaseURL is the odigos_central DSN. Same database the connectors use; the gateway reads the
	// platform schema only.
	DatabaseURL string
	// Namespace scopes the connector and Service watches. Empty watches cluster-wide, which needs
	// cluster-scoped RBAC — prefer setting it.
	Namespace string
	// RevalidateInterval bounds how long a revoked credential can keep an already-open session.
	RevalidateInterval time.Duration
}

// Load reads the environment and validates it.
func Load() (Config, error) {
	c := Config{
		ListenAddr:         env("ODIGOS_OPAMP_GATEWAY_LISTEN_ADDR", ":8080"),
		OpAMPPath:          env("ODIGOS_OPAMP_PATH", "/v1/opamp"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		Namespace:          os.Getenv("ODIGOS_NAMESPACE"),
		RevalidateInterval: gateway.DefaultRevalidateInterval,
	}

	if raw := os.Getenv("ODIGOS_OPAMP_REVALIDATE_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return c, fmt.Errorf("ODIGOS_OPAMP_REVALIDATE_INTERVAL %q: %w", raw, err)
		}
		if d <= 0 {
			// Not a way to disable revalidation. Disabling it would mean a revoked credential keeps its
			// open session — and its configuration pushes — until the Collector chose to reconnect.
			return c, fmt.Errorf("ODIGOS_OPAMP_REVALIDATE_INTERVAL must be positive, got %s", d)
		}
		c.RevalidateInterval = d
	}

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required: the gateway authenticates against %s", "connector_platform.collector_credentials")
	}
	if c.OpAMPPath == "" || c.OpAMPPath[0] != '/' {
		return c, fmt.Errorf("ODIGOS_OPAMP_PATH must be an absolute path, got %q", c.OpAMPPath)
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// MaxDBConns caps the pool. Authentication is one point lookup per handshake, so the pool exists to
// absorb a reconnect storm — the whole fleet arriving at once after a gateway restart — not to run
// long queries.
const MaxDBConns = 10
