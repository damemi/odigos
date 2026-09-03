package instrumentor

import (
	"testing"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// TestNarrowedSignalsHoldsIllegalAndSilentWorkloadsToTheDestination pins the two inputs that must
// NOT narrow. Existing "never widens" tests would still pass if all-none returned (false,false,false):
// that is a narrowing, not a widening, and the observer's equality check would treat a silent agent
// as a legitimate subset. The destination is left unchanged so those cases report as drift.
func TestNarrowedSignalsHoldsIllegalAndSilentWorkloadsToTheDestination(t *testing.T) {
	none := map[string]string{
		EnvTracesExporter:  exporterNone,
		EnvMetricsExporter: exporterNone,
		EnvLogsExporter:    exporterNone,
	}
	tracesOnly := map[string]string{
		EnvMetricsExporter: exporterNone,
		EnvLogsExporter:    exporterNone,
	}

	cases := []struct {
		name                  string
		env                   map[string]string
		traces, metrics, logs bool
		wantT, wantM, wantL   bool
	}{
		{
			name:   "all exporters none against a traces destination is drift, not an empty subset",
			env:    none,
			traces: true,
			wantT:  true,
			wantM:  false,
			wantL:  false,
		},
		{
			name:    "all exporters none against a full destination is drift, not an empty subset",
			env:     none,
			traces:  true,
			metrics: true,
			logs:    true,
			wantT:   true,
			wantM:   true,
			wantL:   true,
		},
		{
			name:   "exporting metrics a traces-only destination cannot receive is drift",
			env:    map[string]string{EnvLogsExporter: exporterNone},
			traces: true,
			wantT:  true,
			wantM:  false,
			wantL:  false,
		},
		{
			name:   "absent exporters default to otlp, so a traces-only dest plus default metrics is drift",
			env:    map[string]string{},
			traces: true,
			wantT:  true,
			wantM:  false,
			wantL:  false,
		},
		{
			name:    "a real subset (traces only, dest accepts all) is held to what the workload exports",
			env:     tracesOnly,
			traces:  true,
			metrics: true,
			logs:    true,
			wantT:   true,
			wantM:   false,
			wantL:   false,
		},
		{
			name:    "explicit otlp on the dest's only signal, others none, is a real subset",
			env:     map[string]string{EnvTracesExporter: exporterOTLP, EnvMetricsExporter: exporterNone, EnvLogsExporter: exporterNone},
			traces:  true,
			metrics: true,
			wantT:   true,
			wantM:   false,
			wantL:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotT, gotM, gotL := narrowedSignals(c.env, c.traces, c.metrics, c.logs)
			if gotT != c.wantT || gotM != c.wantM || gotL != c.wantL {
				t.Errorf("narrowedSignals(%v, %v,%v,%v) = (%v,%v,%v), want (%v,%v,%v)",
					c.env, c.traces, c.metrics, c.logs, gotT, gotM, gotL, c.wantT, c.wantM, c.wantL)
			}
		})
	}
}

func TestSignalExportedTreatsAbsenceAsOTLP(t *testing.T) {
	if !signalExported(nil, EnvTracesExporter) {
		t.Error("a missing map must count as exported — the SDK default is otlp")
	}
	if !signalExported(map[string]string{}, EnvTracesExporter) {
		t.Error("an absent variable must count as exported — the SDK default is otlp")
	}
	if signalExported(map[string]string{EnvTracesExporter: exporterNone}, EnvTracesExporter) {
		t.Error("OTEL_TRACES_EXPORTER=none must count as not exported")
	}
	if !signalExported(map[string]string{EnvTracesExporter: exporterOTLP}, EnvTracesExporter) {
		t.Error("an explicit otlp value must count as exported")
	}
}

func TestNarrowToExportedSignalsLeavesTransportUntouchedOnDrift(t *testing.T) {
	dest := domaindest.OTLPTarget{
		Endpoint:     "collector:4317",
		HTTPEndpoint: "https://collector:4318",
		Insecure:     true,
		Traces:       true,
		Metrics:      true,
		Logs:         true,
	}
	got := narrowToExportedSignals(dest, map[string]string{
		EnvTracesExporter:  exporterNone,
		EnvMetricsExporter: exporterNone,
		EnvLogsExporter:    exporterNone,
	})
	if !got.Traces || !got.Metrics || !got.Logs {
		t.Errorf("silent export must leave the destination signal set untouched, got %+v", got)
	}
	if got.Endpoint != dest.Endpoint || got.HTTPEndpoint != dest.HTTPEndpoint || got.Insecure != dest.Insecure {
		t.Errorf("narrowing altered the transport: %+v -> %+v", dest, got)
	}
}
