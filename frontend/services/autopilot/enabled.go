// Package autopilot serves the call trees autopilot builds per transaction, read
// from insights, as GraphQL models for the Profiling page's "By transaction" view.
package autopilot

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/services"
)

// IsEnabled reports whether autopilot is enabled in effective config.
func IsEnabled(ctx context.Context, c client.Client) (bool, error) {
	cfg, err := services.GetEffectiveConfig(ctx, c)
	if err != nil {
		return false, err
	}
	return EnabledFromOdigosConfig(cfg), nil
}

// EnabledFromOdigosConfig reports whether autopilot is enabled for a loaded config.
func EnabledFromOdigosConfig(cfg *common.OdigosConfiguration) bool {
	return cfg.AutopilotEnabled()
}
