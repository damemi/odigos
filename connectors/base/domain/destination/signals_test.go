package destination

import (
	"fmt"
	"testing"
)

func TestSignalPolicyMatrix(t *testing.T) {
	for bits := 0; bits < 16; bits++ {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			target := OTLPTarget{Traces: bits&1 != 0, Metrics: bits&2 != 0, Logs: bits&4 != 0, Profiles: bits&8 != 0}
			for _, lang := range []string{"java", "nodejs", "python", "dotnet", "php", "ruby"} {
				env := target.SignalActivation(lang)
				if !target.SignalConfigurationMatches(env, lang) {
					t.Fatal("policy does not match itself")
				}
				for key := range env {
					original := env[key]
					env[key] = "invalid"
					if target.SignalConfigurationMatches(env, lang) {
						t.Fatalf("%s: ignored drift in %s", lang, key)
					}
					env[key] = original
				}
				for _, key := range SignalTransportOverrides() {
					env[key] = "https://stale.example"
					if target.SignalConfigurationMatches(env, lang) {
						t.Fatalf("accepted override %s", key)
					}
					delete(env, key)
				}
			}
		})
	}
}

func TestProfilesSelectionDoesNotEnableTraces(t *testing.T) {
	for _, signals := range [][]string{{SignalProfiles}, {SignalTraces, SignalMetrics, SignalLogs, SignalProfiles}} {
		target, ok := ResolveOTLPTarget([]Destination{{Type: "otlp", Config: map[string]any{configOTLPGRPCEndpoint: "collector:4317"}, Signals: signals}})
		if !ok || !target.Profiles || target.Traces != (len(signals) > 1) {
			t.Fatalf("lost signal selection: %+v", target)
		}
	}
}
