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
// instrumentation.Factory directly; instead it hands out purpose-built factories:
//
//   - TracesFactory   - for the OBI distro itself: OBI attaches traces (and network metrics when
//     enabled by the workload's InstrumentationRule). OBI owns the process's status.
//   - MetricsFactory  - for natively-instrumented distros (Java, Python, Node, ...): OBI attaches
//     only network metrics. The native agent owns the process's status, so OBI's instrumentation
//     is reported as auxiliary (no InstrumentationInstance is created for it).
//   - Wrap(base)      - for other eBPF distros (e.g. Go): the base factory owns traces and status,
//     and OBI rides along to add network metrics.
//
// All three drive the same shared instrumenter through the dynamic PID selector. PID selection
// updates are not synchronized here; they are invoked from the instrumentation manager event loop
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

// TracesFactory returns the factory for the OBI distro: OBI attaches traces and, when enabled,
// network metrics. The resulting instrumentation owns the process's reported status.
func (m *Manager) TracesFactory() instrumentation.Factory {
	return &factory{manager: m, traces: true}
}

// MetricsFactory returns a factory that attaches only OBI network metrics to a process that is
// instrumented natively by another agent. The native agent owns and reports the process's status,
// so OBI's status is suppressed (no duplicate InstrumentationInstance is produced).
func (m *Manager) MetricsFactory() instrumentation.Factory {
	return &factory{
		manager: m,
		suppression: &instrumentation.ReportSuppression{
			Reason:  "supplemental OBI network metrics; the process's instrumentation status is owned by its native agent",
			OwnedBy: "native instrumentation agent",
		},
	}
}

// Wrap returns a factory that runs base (e.g. the Go eBPF instrumentation) and additionally attaches
// OBI network metrics. The base instrumentation owns traces and the reported status.
func (m *Manager) Wrap(base instrumentation.Factory) instrumentation.Factory {
	return &factory{manager: m, base: base}
}

// Run waits until ctx is canceled, then stops the OBI instrumenter.
func (m *Manager) Run(ctx context.Context) error {
	<-ctx.Done()
	m.stopInstrumenter()
	return ctx.Err()
}

var _ instrumentation.Factory = (*factory)(nil)

type factory struct {
	manager *Manager
	// traces indicates OBI should attach its own trace probes (the OBI distro).
	traces bool
	// base is an optional wrapped factory (e.g. Go) that owns traces and status for the process.
	base instrumentation.Factory
	// suppression, when non-nil, suppresses the reported status of instrumentations created without
	// a base factory. When a base factory is wrapped, the base owns the reported status and this is
	// unused.
	suppression *instrumentation.ReportSuppression
}

func (f *factory) CreateInstrumentation(ctx context.Context, pid int, settings instrumentation.Settings) (instrumentation.Instrumentation, error) {
	var base instrumentation.Instrumentation
	if f.base != nil {
		b, err := f.base.CreateInstrumentation(ctx, pid, settings)
		if err != nil {
			return nil, err
		}
		base = b
	}

	return &processInstrumentation{
		manager:        f.manager,
		pid:            pid,
		traces:         f.traces,
		base:           base,
		networkMetrics: networkMetricsEnabled(settings.InitialConfig),
		suppression:    f.suppression,
	}, nil
}

type processInstrumentation struct {
	manager *Manager
	pid     int

	traces         bool
	networkMetrics bool
	suppression    *instrumentation.ReportSuppression
	base           instrumentation.Instrumentation
}

func (p *processInstrumentation) Load(ctx context.Context) (instrumentation.Status, error) {
	var status instrumentation.Status
	if p.base != nil {
		s, err := p.base.Load(ctx)
		if err != nil {
			// The owning instrumentation failed to load; do not attach OBI metrics to it.
			return s, err
		}
		// The base instrumentation owns the process's reported status.
		status = s
	} else {
		status.Suppression = p.suppression
	}

	if p.traces || p.networkMetrics {
		p.manager.ensureInstrumenterRunning()
		if p.traces {
			p.manager.selector.Traces().AddPIDs(uint32(p.pid))
		}
		if p.networkMetrics {
			p.manager.selector.NetworkMetrics().AddPIDs(uint32(p.pid))
			p.manager.selector.StatsMetrics().AddPIDs(uint32(p.pid))
		}
	}

	return status, nil
}

func (p *processInstrumentation) Run(ctx context.Context) error {
	if p.base != nil {
		return p.base.Run(ctx)
	}
	<-ctx.Done()
	return nil
}

func (p *processInstrumentation) Close(ctx context.Context) error {
	var err error
	if p.base != nil {
		err = p.base.Close(ctx)
	}
	if p.traces {
		p.manager.selector.Traces().RemovePIDs(uint32(p.pid))
	}
	p.manager.removeNetworkMetricsPIDs(p.pid)
	p.manager.maybeStopInstrumenter()
	return err
}

func (p *processInstrumentation) ApplyConfig(ctx context.Context, config instrumentation.Config) error {
	var err error
	if p.base != nil {
		err = p.base.ApplyConfig(ctx, config)
	}

	p.networkMetrics = networkMetricsEnabled(config)
	p.manager.setNetworkMetrics(p.pid, p.networkMetrics)
	if !p.traces && !p.networkMetrics && p.base == nil {
		p.manager.maybeStopInstrumenter()
	}
	return err
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
