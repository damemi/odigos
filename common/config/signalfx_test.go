package config

import (
	"testing"

	"github.com/odigos-io/odigos/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSignalFxConfig() Config {
	return Config{Exporters: GenericMap{}, Service: Service{Pipelines: map[string]Pipeline{}}}
}

// signalfx.yaml declares traces as a supported signal, so a destination created for traces must
// produce a traces pipeline. The signalfx exporter carries metrics and events only, so traces
// reach the same realm over OTLP/HTTP.
func TestSignalFxTracesEnabled(t *testing.T) {
	for _, realm := range []string{"us0", "eu0"} {
		t.Run(realm, func(t *testing.T) {
			dest := &mockDestination{
				id:      "signalfx.example",
				config:  map[string]string{signalfxRealm: realm},
				signals: []common.ObservabilitySignal{common.TracesObservabilitySignal},
			}
			cfg := newSignalFxConfig()

			pipelines, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
			require.NoError(t, err)

			assert.Equal(t, []string{"traces/signalfx-signalfx.example"}, pipelines)
			assert.Equal(t, GenericMap{
				"traces_endpoint": "https://ingest." + realm + ".signalfx.com/v2/trace/otlp",
				"headers": GenericMap{
					"X-SF-Token": "${ODIGOS_DEST_signalfx_example_SIGNALFX_ACCESS_TOKEN}",
				},
			}, cfg.Exporters["otlp_http/signalfx-signalfx.example"])
			assert.Equal(t,
				[]string{"otlp_http/signalfx-signalfx.example"},
				cfg.Service.Pipelines["traces/signalfx-signalfx.example"].Exporters,
			)
		})
	}
}

// A destination that only selected traces must not export metrics to SignalFx.
func TestSignalFxTracesOnlyDoesNotExportMetrics(t *testing.T) {
	dest := &mockDestination{
		id:      "signalfx.example",
		config:  map[string]string{signalfxRealm: "us0"},
		signals: []common.ObservabilitySignal{common.TracesObservabilitySignal},
	}
	cfg := newSignalFxConfig()

	pipelines, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	assert.NotContains(t, pipelines, "metrics/signalfx-signalfx.example")
	assert.NotContains(t, cfg.Service.Pipelines, "metrics/signalfx-signalfx.example")
	assert.NotContains(t, cfg.Exporters, "signalfx/signalfx.example")
}

func TestSignalFxMetricsEnabled(t *testing.T) {
	dest := &mockDestination{
		id:      "signalfx.example",
		config:  map[string]string{signalfxRealm: "us1"},
		signals: []common.ObservabilitySignal{common.MetricsObservabilitySignal},
	}
	cfg := newSignalFxConfig()

	pipelines, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	assert.Equal(t, []string{"metrics/signalfx-signalfx.example"}, pipelines)
	assert.Equal(t, GenericMap{
		"access_token": "${ODIGOS_DEST_signalfx_example_SIGNALFX_ACCESS_TOKEN}",
		"realm":        "us1",
		"api_url":      "https://api.us1.signalfx.com",
		"ingest_url":   "https://ingest.us1.signalfx.com",
	}, cfg.Exporters["signalfx/signalfx.example"])
	assert.NotContains(t, cfg.Service.Pipelines, "traces/signalfx-signalfx.example")
	assert.NotContains(t, cfg.Exporters, "otlp_http/signalfx-signalfx.example")
}

func TestSignalFxTracesAndMetricsEnabled(t *testing.T) {
	dest := &mockDestination{
		id:     "signalfx.example",
		config: map[string]string{signalfxRealm: "us0"},
		signals: []common.ObservabilitySignal{
			common.TracesObservabilitySignal,
			common.MetricsObservabilitySignal,
		},
	}
	cfg := newSignalFxConfig()

	pipelines, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"metrics/signalfx-signalfx.example",
		"traces/signalfx-signalfx.example",
	}, pipelines)
	assert.Contains(t, cfg.Exporters, "signalfx/signalfx.example")
	assert.Contains(t, cfg.Exporters, "otlp_http/signalfx-signalfx.example")
}

// logs is declared unsupported in signalfx.yaml, so a logs-only destination configures nothing.
func TestSignalFxLogsOnlyConfiguresNothing(t *testing.T) {
	dest := &mockDestination{
		id:      "signalfx.example",
		config:  map[string]string{signalfxRealm: "us0"},
		signals: []common.ObservabilitySignal{common.LogsObservabilitySignal},
	}
	cfg := newSignalFxConfig()

	pipelines, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	assert.Empty(t, pipelines)
	assert.Empty(t, cfg.Exporters)
	assert.Empty(t, cfg.Service.Pipelines)
}

// The TLS settings are destination wide, so both exporters must honour them, each with its own map.
func TestSignalFxTLSAppliesToEveryExporter(t *testing.T) {
	dest := &mockDestination{
		id: "signalfx.example",
		config: map[string]string{
			signalfxRealm:              "us0",
			signalfxInsecureSkipVerify: "true",
		},
		signals: []common.ObservabilitySignal{
			common.TracesObservabilitySignal,
			common.MetricsObservabilitySignal,
		},
	}
	cfg := newSignalFxConfig()

	_, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	metricsTLS := cfg.Exporters["signalfx/signalfx.example"].(GenericMap)["tls"].(GenericMap)
	tracesTLS := cfg.Exporters["otlp_http/signalfx-signalfx.example"].(GenericMap)["tls"].(GenericMap)
	assert.Equal(t, GenericMap{"insecure_skip_verify": true}, metricsTLS)
	assert.Equal(t, GenericMap{"insecure_skip_verify": true}, tracesTLS)

	// the two exporters must not share one map, or a later edit to either would alter both
	metricsTLS["insecure_skip_verify"] = false
	assert.Equal(t, GenericMap{"insecure_skip_verify": true}, tracesTLS)
}

// SIGNALFX_CA_PEM is a secret field, so it is stored in the Destination Secret and is not visible
// in GetConfig. A supplied CA must therefore be gated on the non-secret SIGNALFX_USE_CUSTOM_CA and
// read through the destination-scoped env var, on both exporters.
func TestSignalFxCustomCAReachesEveryExporter(t *testing.T) {
	dest := &mockDestination{
		id: "signalfx.example",
		config: map[string]string{
			signalfxRealm:       "us0",
			signalfxUseCustomCa: "true",
		},
		signals: []common.ObservabilitySignal{
			common.TracesObservabilitySignal,
			common.MetricsObservabilitySignal,
		},
	}
	cfg := newSignalFxConfig()

	_, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	want := GenericMap{
		"insecure_skip_verify": false,
		"ca_pem":               "${ODIGOS_DEST_signalfx_example_SIGNALFX_CA_PEM}",
	}
	for _, exporter := range []string{"signalfx/signalfx.example", "otlp_http/signalfx-signalfx.example"} {
		tls, ok := cfg.Exporters[exporter].(GenericMap)["tls"].(GenericMap)
		require.True(t, ok, "%s has no tls block", exporter)
		assert.Equal(t, want, tls, exporter)
		// ca_file would point at a path nothing mounts into the gateway pod.
		assert.NotContains(t, tls, "ca_file", exporter)
	}
}

// The certificate lives in the Secret, so leaving the checkbox off must not emit a ca reference
// the collector would then fail to resolve.
func TestSignalFxCustomCANotRequestedEmitsNoCA(t *testing.T) {
	dest := &mockDestination{
		id: "signalfx.example",
		config: map[string]string{
			signalfxRealm:              "us0",
			signalfxInsecureSkipVerify: "true",
		},
		signals: []common.ObservabilitySignal{common.MetricsObservabilitySignal},
	}
	cfg := newSignalFxConfig()

	_, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	tls := cfg.Exporters["signalfx/signalfx.example"].(GenericMap)["tls"].(GenericMap)
	assert.Equal(t, GenericMap{"insecure_skip_verify": true}, tls)
}

// A custom CA and skipped verification are independent, so both can be set at once.
func TestSignalFxCustomCAWithInsecureSkipVerify(t *testing.T) {
	dest := &mockDestination{
		id: "signalfx.example",
		config: map[string]string{
			signalfxRealm:              "us0",
			signalfxUseCustomCa:        "true",
			signalfxInsecureSkipVerify: "true",
		},
		signals: []common.ObservabilitySignal{common.MetricsObservabilitySignal},
	}
	cfg := newSignalFxConfig()

	_, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	assert.Equal(t, GenericMap{
		"insecure_skip_verify": true,
		"ca_pem":               "${ODIGOS_DEST_signalfx_example_SIGNALFX_CA_PEM}",
	}, cfg.Exporters["signalfx/signalfx.example"].(GenericMap)["tls"])
}

func TestSignalFxNoTLSCustomizationLeavesExporterDefaults(t *testing.T) {
	dest := &mockDestination{
		id:      "signalfx.example",
		config:  map[string]string{signalfxRealm: "us0"},
		signals: []common.ObservabilitySignal{common.MetricsObservabilitySignal},
	}
	cfg := newSignalFxConfig()

	_, err := (&SignalFx{}).ModifyConfig(dest, &cfg)
	require.NoError(t, err)

	assert.NotContains(t, cfg.Exporters["signalfx/signalfx.example"], "tls")
}
