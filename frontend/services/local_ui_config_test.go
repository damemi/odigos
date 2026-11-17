package services

import (
	"testing"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/frontend/graph/model"
)

func TestApplyAutopilotInput_ModeAndReset(t *testing.T) {
	enabled := true
	cfg := &common.OdigosConfiguration{
		Autopilot: &common.AutopilotConfiguration{Enabled: &enabled, Mode: common.AutopilotModeAuto},
	}
	off := model.AutopilotModeOff
	applyAutopilotInput(cfg, &model.LocalUIConfigAutopilotInput{Mode: &off})
	if cfg.Autopilot.Mode != common.AutopilotModeOff {
		t.Fatalf("mode = %q, want off", cfg.Autopilot.Mode)
	}
	if cfg.Autopilot.Enabled == nil || !*cfg.Autopilot.Enabled {
		t.Fatal("enabled was cleared")
	}

	applyAutopilotInput(cfg, &model.LocalUIConfigAutopilotInput{ResetPaths: []string{"interrogation.mode"}})
	if cfg.Autopilot.Mode != "" {
		t.Fatalf("mode = %q, want empty after reset", cfg.Autopilot.Mode)
	}
	if cfg.Autopilot.Enabled == nil || !*cfg.Autopilot.Enabled {
		t.Fatal("reset cleared enabled")
	}
}
