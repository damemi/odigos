package obi

import (
	"context"
	"fmt"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	"github.com/odigos-io/odigos/common/consts"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/instrumentation"

	"go.opentelemetry.io/obi/pkg/appolly/discover"
	obiconfig "go.opentelemetry.io/obi/pkg/config"
	"go.opentelemetry.io/obi/pkg/export"
	"go.opentelemetry.io/obi/pkg/export/instrumentations"
	"go.opentelemetry.io/obi/pkg/instrumenter"
	obipkg "go.opentelemetry.io/obi/pkg/obi"
)

// DistroName is the Odigos Otel distribution name for OBI trace instrumentation.
const DistroName = "opentelemetry-ebpf-instrumentation"

// Manager owns the shared OBI instrumenter and its dynamic PID selector. It does not implement
// instrumentation.Factory directly; instead it hands out two purpose-built factories:
//
//   - TracesFactory   - the primary factory for the OBI distro. It attaches OBI trace probes only.
//     As the OBI distro's explicit instrumentation it reports status normally, like any other distro.
//   - MetricsFactory  - registered as pre-instrument middleware in the instrumentation manager. It
//     attaches OBI network + TCP stats metrics to any process, gated per-workload by the
//     networkMetrics InstrumentationRule. Middleware is never reported.
//
// Both drive the same shared instrumenter through the dynamic PID selector. PID selection updates
// are not synchronized here; they are invoked from the instrumentation manager event loop
// (Load/Close/ApplyConfig), which processes one event at a time.
type Manager struct {
	logger *commonlogger.OdigosLogger
	obiCfg *obipkg.Config

	selector *discover.DynamicPIDSelector

	runCtx    context.Context
	runCancel context.CancelFunc
}

// NewManager creates a manager with a fresh dynamic PID selector.
func NewManager() *Manager {
	return &Manager{
		selector: discover.NewDynamicPIDSelector(),
		obiCfg:   obiConfigForOdigos(),
		logger:   commonlogger.LoggerCompat().With("subsystem", "opentelemetry-ebpf-instrumentation"),
	}
}

// TracesFactory returns the primary factory for the OBI distro. It attaches OBI trace probes only.
// As the OBI distro's explicit instrumentation, it reports status like any other distro's factory.
func (m *Manager) TracesFactory() instrumentation.Factory {
	return &tracesFactory{manager: m}
}

// MetricsFactory returns the factory registered as pre-instrument middleware. It attaches OBI
// network + TCP stats metrics to a process, gated per-workload by the networkMetrics
// InstrumentationRule. Its status is never reported.
func (m *Manager) MetricsFactory() instrumentation.Factory {
	return &metricsFactory{manager: m}
}

// Run waits until ctx is canceled, then stops the OBI instrumenter.
func (m *Manager) Run(ctx context.Context) error {
	<-ctx.Done()
	m.stopInstrumenter()
	return ctx.Err()
}

var (
	_ instrumentation.Factory = (*tracesFactory)(nil)
	_ instrumentation.Factory = (*metricsFactory)(nil)
)

// tracesFactory is the OBI distro's primary factory.
type tracesFactory struct {
	manager *Manager
}

func (f *tracesFactory) CreateInstrumentation(_ context.Context, pid int, _ instrumentation.Settings) (instrumentation.Instrumentation, error) {
	return &tracesInstrumentation{manager: f.manager, pid: pid}, nil
}

type tracesInstrumentation struct {
	manager *Manager
	pid     int
}

func (t *tracesInstrumentation) Load(context.Context) (instrumentation.Status, error) {
	t.manager.ensureInstrumenterRunning()
	t.manager.selector.Traces().AddPIDs(uint32(t.pid))
	return instrumentation.Status{}, nil
}

func (t *tracesInstrumentation) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (t *tracesInstrumentation) Close(context.Context) error {
	t.manager.selector.Traces().RemovePIDs(uint32(t.pid))
	t.manager.maybeStopInstrumenter()
	return nil
}

func (t *tracesInstrumentation) ApplyConfig(context.Context, instrumentation.Config) error {
	return nil
}

// metricsFactory is registered as pre-instrument middleware; it applies to every process (network
// metrics can be enabled on any workload) and gates attachment by config.
type metricsFactory struct {
	manager *Manager
}

func (f *metricsFactory) CreateInstrumentation(_ context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	return &metricsInstrumentation{
		manager: f.manager,
		pid:     pid,
		enabled: networkMetricsEnabled(settings.InitialConfig),
	}, nil
}

type metricsInstrumentation struct {
	manager *Manager
	pid     int
	enabled bool
}

func (mi *metricsInstrumentation) Load(context.Context) (instrumentation.Status, error) {
	if mi.enabled {
		mi.manager.ensureInstrumenterRunning()
		mi.manager.selector.NetworkMetrics().AddPIDs(uint32(mi.pid))
		mi.manager.selector.StatsMetrics().AddPIDs(uint32(mi.pid))
	}
	return instrumentation.Status{}, nil
}

func (mi *metricsInstrumentation) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (mi *metricsInstrumentation) Close(context.Context) error {
	mi.manager.removeNetworkMetricsPIDs(mi.pid)
	mi.manager.maybeStopInstrumenter()
	return nil
}

func (mi *metricsInstrumentation) ApplyConfig(_ context.Context, config instrumentation.Config) error {
	mi.enabled = networkMetricsEnabled(config)
	mi.manager.setNetworkMetrics(mi.pid, mi.enabled)
	mi.manager.maybeStopInstrumenter()
	return nil
}

// networkMetricsEnabled reports whether the workload's per-container config enables OBI network metrics.
func networkMetricsEnabled(config instrumentation.Config) bool {
	cc, ok := config.(*odigosv1.ContainerAgentConfig)
	if !ok || cc == nil || cc.Metrics == nil {
		return false
	}
	return instrumentationrules.NetworkMetricsEnabled(cc.Metrics.NetworkMetrics)
}

func (m *Manager) setNetworkMetrics(pid int, enabled bool) {
	if pid <= 0 {
		return
	}
	if !enabled {
		m.removeNetworkMetricsPIDs(pid)
		return
	}
	m.ensureInstrumenterRunning()
	m.selector.NetworkMetrics().AddPIDs(uint32(pid))
	m.selector.StatsMetrics().AddPIDs(uint32(pid))
}

func (m *Manager) removeNetworkMetricsPIDs(pid int) {
	m.selector.NetworkMetrics().RemovePIDs(uint32(pid))
	m.selector.StatsMetrics().RemovePIDs(uint32(pid))
}

func obiConfigForOdigos() *obipkg.Config {
	cfg := obipkg.DefaultConfig
	cfg.EBPF.ContextPropagation = obiconfig.ContextPropagationHeaders

	collectorEndpoint := fmt.Sprintf("http://localhost:%d", consts.OTLPPort)
	cfg.Traces.TracesEndpoint = collectorEndpoint
	cfg.OTELMetrics.MetricsEndpoint = collectorEndpoint

	cfg.Traces.Instrumentations = append(cfg.Traces.Instrumentations, instrumentations.InstrumentationDNS)

	cfg.Metrics.Features = export.FeatureNetwork | export.FeatureStats

	return &cfg
}

func (m *Manager) ensureInstrumenterRunning() {
	if m.runCancel != nil {
		return
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	obiCfg := m.obiCfg
	m.runCtx = runCtx
	m.runCancel = runCancel

	go func() {
		err := instrumenter.Run(runCtx, obiCfg, instrumenter.WithDynamicPIDSelector(m.selector))
		if err != nil && runCtx.Err() == nil {
			m.logger.Error("OBI instrumenter exited with error", "err", err)
		}
	}()
}

func (m *Manager) maybeStopInstrumenter() {
	if m.runCancel == nil || m.hasAnySelectedPIDs() {
		return
	}
	m.stopInstrumenter()
}

func (m *Manager) stopInstrumenter() {
	if m.runCancel == nil {
		return
	}
	m.runCancel()
	m.runCancel = nil
	m.runCtx = nil
}

func (m *Manager) hasAnySelectedPIDs() bool {
	if _, ok := m.selector.Traces().GetPIDs(); ok {
		return true
	}
	if _, ok := m.selector.NetworkMetrics().GetPIDs(); ok {
		return true
	}
	if _, ok := m.selector.StatsMetrics().GetPIDs(); ok {
		return true
	}
	return false
}
