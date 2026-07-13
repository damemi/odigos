package instrumentation

import (
	"context"
	"errors"
	"fmt"
	"time"

	cilumebpf "github.com/cilium/ebpf"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"golang.org/x/sync/errgroup"

	"github.com/odigos-io/odigos/common/unixfd"
	"github.com/odigos-io/odigos/distros/distro"
	"github.com/odigos-io/odigos/instrumentation/detector"
)

var (
	errNoInstrumentationFactory = errors.New("no ebpf factory found")
	errFailedToGetDetails       = errors.New("failed to get details for process event")
	errFailedToGetDistribution  = errors.New("failed to get otel distribution for details")
	errFailedToGetConfigGroup   = errors.New("failed to get config group")
	errFailedToGetProcessGroup  = errors.New("failed to get process group")
)

const (
	shutdownCleanupTimeout = 10 * time.Second
	otelMeterName          = "github.com/odigos.io/odigos/instrumentation"
)

var meter = otel.Meter(otelMeterName)

// ConfigUpdate is used to send a configuration update request to the manager.
// The manager will apply the configuration to all instrumentations that match the config group.
type ConfigUpdate[configGroup ConfigGroup] map[configGroup]Config

// Request is used to send an instrumentation, un-instrumentation, or retry-failed request to
// the manager.
//
// For instrumentation requests, set Instrument=true and populate ProcessDetailsByPid with the
// details of each process to instrument. For un-instrumentation requests, set Instrument=false
// and populate ProcessGroup to un-instrument all processes that match it (the manager keeps an
// index of instrumented processes by process group to make this efficient).
//
// For retry-failed requests, set RetryDistros to a non-nil slice; the manager will then iterate
// over its tracked instrumentations and retry any whose distro factory failed to initialize/load
// AND whose OTel distribution name matches one of the supplied values. A non-nil empty slice
// retries every failed distro factory regardless of distribution. When RetryDistros is non-nil the
// Instrument / ProcessDetailsByPid / ProcessGroup fields are ignored.
type Request[processGroup ProcessGroup, configGroup ConfigGroup, processDetails ProcessDetails[processGroup, configGroup]] struct {
	Instrument          bool
	ProcessDetailsByPid map[int]processDetails
	ProcessGroup        processGroup
	RetryDistros        []string
}

type instrumentationDetails[processGroup ProcessGroup, configGroup ConfigGroup, processDetails ProcessDetails[processGroup, configGroup]] struct {
	// insts is keyed by the name of the factory that produced the entry: a distro factory under its
	// distribution name, a generic factory (e.g. OBI network metrics, eBPF log capture, which apply
	// to every process regardless of distro) under its registered name. A non-nil value is a loaded
	// instrumentation; a nil value marks a factory that was attempted but failed to init/load - the
	// pid is still tracked so the reporter is notified on exit, and the factory stays a retry
	// candidate. A factory that opted out (created no instrumentation) has no entry. Values are run,
	// reconfigured and closed uniformly, guarding for nil; whether each reports its lifecycle is
	// decided per-instrumentation by the Status.SkipReport it returns from Load.
	//
	// The map is the single source of truth for load state, with no separate flag: "is the distro
	// loaded?" is a non-nil lookup of the distribution name (which is also what a RetryDistros request
	// names). A retry re-runs whatever is not loaded (a nil or absent entry), leaving loaded ones
	// untouched. Factory names share one namespace, so a generic factory must not be registered under
	// a distribution name.
	insts map[string]Instrumentation

	pd processDetails
	cg configGroup
	pg processGroup
}

type ManagerOptions[processGroup ProcessGroup, configGroup ConfigGroup, processDetails ProcessDetails[processGroup, configGroup]] struct {
	// Factories maps Odigos Otel distribution names to their instrumentation factories.
	//
	// The manager uses this map to create the instrumentation selected by a process's distribution.
	// A distribution with no entry here simply has no factory of its own; the process may still be
	// instrumented by GenericFactories. If neither applies, the event is ignored.
	Factories map[string]Factory

	// GenericFactories maps a name to a factory that applies to every process, in addition to
	// (and independently of) the factory selected by the process's distribution - including processes
	// whose distribution has no factory at all. This is how cross-cutting eBPF signals like OBI
	// network metrics and eBPF log capture are attached uniformly. Names share a namespace with the
	// distribution names in Factories, so a generic factory must not reuse a distribution name.
	//
	// Each is asked to create an Instrumentation via CreateInstrumentation and may return (nil, nil)
	// to opt out of a given process. They are loaded, run, reconfigured and closed exactly like any
	// other instrumentation; whether each reports its lifecycle is decided per-instrumentation by
	// the Status.SkipReport it returns from Load.
	GenericFactories map[string]Factory

	// Handler is used to resolve details, config group, OTel distribution and settings for the instrumentation
	// based on the process event.
	//
	// The handler is also used to report the instrumentation lifecycle events.
	Handler *Handler[processGroup, configGroup, processDetails]

	// DetectorOptions is a list of options to configure the process detector.
	//
	// The process detector is used to trigger new instrumentation for new relevant processes,
	// and un-instrumenting processes once they exit.
	DetectorOptions []detector.DetectorOption

	// ConfigUpdates is a channel for receiving configuration updates.
	// The manager will apply the configuration to all instrumentations that match the config group.
	//
	// The caller is responsible for closing the channel once no more updates are expected.
	ConfigUpdates <-chan ConfigUpdate[configGroup]

	// InstrumentationRequests is a channel for receiving explicit instrumentation, un-
	// instrumentation, or retry-failed requests. See the Request docs for the encoding of each
	// request type.
	InstrumentationRequests <-chan Request[processGroup, configGroup, processDetails]

	// TracesMap is the optional common eBPF map that will be used to send events from eBPF probes.
	TracesMap *cilumebpf.Map

	// MetricsMap is the optional common eBPF map that is used to read metrics per Java process at each interval.
	MetricsMap *cilumebpf.Map

	// MetricsAttributesMap is the optional eBPF Hash map for UUID -> packed resource attributes.
	// Used alongside MetricsMap to store resource attributes separately from the metrics hash key.
	MetricsAttributesMap *cilumebpf.Map

	// Logger is optional. When set, the manager uses it; otherwise it uses commonlogger.LoggerCompat().With("subsystem", "ebpfmanager").
	Logger *commonlogger.OdigosLogger

	// LogsMap is the optional common eBPF map that will be used to send log events from eBPF probes.
	LogsMap *cilumebpf.Map

	// LogsAttrSubscribe streams per-process resource attributes over the logs unix socket.
	LogsAttrSubscribe func() (updates <-chan string, snapshot []string)
}

// Manager is used to orchestrate the ebpf instrumentations lifecycle.
type Manager interface {
	// Run launches the manger.
	// It will block until the context is canceled.
	// It is an error to not cancel the context before the program exits, and may result in leaked resources.
	Run(ctx context.Context) error
}

type manager[processGroup ProcessGroup, configGroup ConfigGroup, processDetails ProcessDetails[processGroup, configGroup]] struct {
	// channel for receiving process events,
	// used to detect new processes and process exits, and handle their instrumentation accordingly.
	procEvents       <-chan detector.ProcessEvent
	detector         detector.Detector
	handler          *Handler[processGroup, configGroup, processDetails]
	factories        map[string]Factory
	genericFactories map[string]Factory
	logger           *commonlogger.OdigosLogger

	// all the created instrumentations by pid,
	// this map is not concurrent safe, so it should be accessed only from the main event loop
	detailsByPid map[int]*instrumentationDetails[processGroup, configGroup, processDetails]

	// instrumentations by config group, and aggregated by pid
	// this map is not concurrent safe, so it should be accessed only from the main event loop
	detailsByConfigGroup map[configGroup]map[int]*instrumentationDetails[processGroup, configGroup, processDetails]

	// instrumentations by process group, and aggregated by pid
	// this map is not concurrent safe, so it should be accessed only from the main event loop
	detailsByProcessGroup map[processGroup]map[int]*instrumentationDetails[processGroup, configGroup, processDetails]

	configUpdates <-chan ConfigUpdate[configGroup]

	requests <-chan Request[processGroup, configGroup, processDetails]

	metrics *managerMetrics

	tracesMap            *cilumebpf.Map
	metricsMap           *cilumebpf.Map
	metricsAttributesMap *cilumebpf.Map
	logsMap              *cilumebpf.Map
	logsAttrSubscribe    func() (updates <-chan string, snapshot []string)
}

func NewManager[processGroup ProcessGroup, configGroup ConfigGroup, processDetails ProcessDetails[processGroup, configGroup]](options ManagerOptions[processGroup, configGroup, processDetails]) (Manager, error) {
	handler := options.Handler
	if handler == nil {
		return nil, errors.New("handler is required for ebpf instrumentation manager")
	}

	if handler.Reporter == nil {
		return nil, errors.New("reporter is required for ebpf instrumentation manager")
	}

	if handler.ProcessDetailsResolver == nil {
		return nil, errors.New("details resolver is required for ebpf instrumentation manager")
	}

	if handler.SettingsGetter == nil {
		return nil, errors.New("settings getter is required for ebpf instrumentation manager")
	}

	if options.ConfigUpdates == nil {
		return nil, errors.New("config updates channel is required for ebpf instrumentation manager")
	}

	managerMetrics, err := newManagerMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("failed to create ebpf instrumentation manager metrics: %w", err)
	}

	logger := commonlogger.LoggerCompat().With("subsystem", "ebpfmanager")
	if options.Logger != nil {
		logger = options.Logger
	}
	procEvents := make(chan detector.ProcessEvent)
	detector, err := detector.NewDetector(procEvents, options.DetectorOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to create process detector: %w", err)
	}

	return &manager[processGroup, configGroup, processDetails]{
		procEvents:            procEvents,
		detector:              detector,
		handler:               handler,
		factories:             options.Factories,
		genericFactories:      options.GenericFactories,
		logger:                logger,
		detailsByPid:          make(map[int]*instrumentationDetails[processGroup, configGroup, processDetails]),
		detailsByConfigGroup:  map[configGroup]map[int]*instrumentationDetails[processGroup, configGroup, processDetails]{},
		detailsByProcessGroup: map[processGroup]map[int]*instrumentationDetails[processGroup, configGroup, processDetails]{},
		configUpdates:         options.ConfigUpdates,
		requests:              options.InstrumentationRequests,
		metrics:               managerMetrics,
		tracesMap:             options.TracesMap,
		metricsMap:            options.MetricsMap,
		metricsAttributesMap:  options.MetricsAttributesMap,
		logsMap:               options.LogsMap,
		logsAttrSubscribe:     options.LogsAttrSubscribe,
	}, nil
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) runEventLoop(ctx context.Context) {
	// cleanup all instrumentations on shutdown
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownCleanupTimeout)
		defer cancel()

		for pid, details := range m.detailsByPid {
			select {
			case <-ctx.Done():
				m.logger.Error("context canceled while cleaning up instrumentations before shutdown", "err", ctx.Err())
				return
			default:
				for _, inst := range details.insts {
					if inst == nil {
						// a factory that failed to init/load is tracked as a nil entry
						continue
					}
					if err := inst.Close(ctx); err != nil {
						m.logger.Error("failed to close instrumentation", "err", err, "pid", pid)
					}
				}
				if err := m.handler.Reporter.OnExit(ctx, pid, details.pd); err != nil {
					m.logger.Error("failed to report instrumentation exit", "err", err)
				}
			}
		}

		m.detailsByPid = nil
		m.detailsByConfigGroup = nil
		m.detailsByProcessGroup = nil
		m.logger.Info("all instrumentations cleaned up")
	}()

	// main event loop for handling instrumentations
	for {
		select {
		case <-ctx.Done():
			m.logger.Info("stopping eBPF instrumentation manager")
			return
		case e, ok := <-m.procEvents:
			if !ok {
				m.logger.Info("process events channel closed, stopping eBPF instrumentation manager")
				return
			}
			switch e.EventType {
			case detector.ProcessExecEvent, detector.ProcessForkEvent, detector.ProcessFileOpenEvent:
				m.logger.Debug("detected new process", "pid", e.PID, "cmd", e.ExecDetails.CmdLine)
				err := m.tryInstrumentFromProcessEvent(ctx, e)
				if err != nil {
					m.handleInstrumentError(err)
				}
			case detector.ProcessExitEvent:
				m.cleanInstrumentation(ctx, e.PID)
			}
		case req, ok := <-m.requests:
			if !ok {
				m.logger.Info("instrumentation requests channel closed, stopping eBPF instrumentation manager")
				return
			}
			// A non-nil RetryDistros marks this request as a retry-failed signal rather than a
			// normal instrument / un-instrument request. We check it first so the rest of the
			// request fields can be left zero by retry senders.
			if req.RetryDistros != nil {
				m.retryFailedInstrumentationsForDistros(ctx, req.RetryDistros)
				continue
			}
			if req.Instrument {
				m.instrumentFromDetails(ctx, req.ProcessDetailsByPid)
			} else {
				// for un-instrumentation requests, we find all instrumentations that match the process group
				// and clean them up.
				procs, ok := m.detailsByProcessGroup[req.ProcessGroup]
				if !ok {
					continue
				}
				m.logger.Info("received explicit un-instrumentation request", "process group", req.ProcessGroup, "numPIDs", len(procs))
				for pid := range procs {
					m.cleanInstrumentation(ctx, pid)
				}
				// we could add a detector.UntrackProcesses call here, for now this is not necessary
				// reasoning to add it in the future might be to save resources in the detector
				// we might get exit events for already un-instrumented processes, which is a no-op.
			}
		case configUpdate := <-m.configUpdates:
			for configGroup, config := range configUpdate {
				err := m.applyInstrumentationConfigurationForSDK(ctx, configGroup, config)
				if err != nil {
					m.logger.Error("failed to apply instrumentation configuration", "err", err)
				}
			}
		}
	}
}

// instrumentFromDetails runs tryInstrument for each (pid, pd) that is not already instrumented,
// then re-arms the process detector for successes. Duplicate or in-flight requests are skipped via
// isInstrumented; tracked entries whose distro factory failed are retried.
func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) instrumentFromDetails(ctx context.Context, byPid map[int]ProcessDetails) {
	var tracked []int
	for pid, pd := range byPid {
		// Handle duplicate requests gracefully; this can happen when external systems such as
		// k8s controllers re-send instrumentation for an already-live process.
		if m.isInstrumented(ctx, pid) {
			continue
		}
		m.logger.Info("attempting instrumentation", "pid", pid, "process details", pd)
		if err := m.tryInstrument(ctx, pd, pid); err != nil {
			m.handleInstrumentError(err)
			continue
		}
		tracked = append(tracked, pid)
	}
	if len(tracked) > 0 {
		// Let the detector know we want exit events for these processes so we can clean up.
		// TrackProcesses is idempotent for already-tracked PIDs.
		m.detector.TrackProcesses(tracked)
	}
}

// failedProcessDetailsByDistro returns a snapshot of tracked processes whose distro factory failed
// to initialize/load. When distroFilter is non-empty, only entries whose OTel distribution name
// matches one of the supplied values are included; an empty filter retries every failed distro
// factory regardless of distribution.
func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) failedProcessDetailsByDistro(ctx context.Context, distroFilter []string) map[int]ProcessDetails {
	wanted := make(map[string]struct{}, len(distroFilter))
	for _, name := range distroFilter {
		wanted[name] = struct{}{}
	}

	byPid := make(map[int]ProcessDetails)
	// Snapshot into a new map first; tryInstrument re-enters startTrackInstrumentation and
	// mutates detailsByPid for the same pid.
	for pid, details := range m.detailsByPid {
		// Only a distro whose factory has not loaded is a retry candidate: retries are keyed by the
		// requested distributions (RetryDistros, matched below), so a loaded distro, or a process with
		// no distro factory of its own, is not retried here.
		distribution, loaded := m.distroLoadState(ctx, details)
		if distribution == nil || loaded {
			continue
		}
		if len(wanted) > 0 {
			if _, ok := wanted[distribution.Name]; !ok {
				continue
			}
		}
		byPid[pid] = details.pd
	}
	return byPid
}

// retryFailedInstrumentationsForDistros re-attempts instrumentation for failed entries in
// detailsByPid, optionally filtered by OTel distribution name.
func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) retryFailedInstrumentationsForDistros(ctx context.Context, distroFilter []string) {
	byPid := m.failedProcessDetailsByDistro(ctx, distroFilter)
	if len(byPid) == 0 {
		return
	}
	m.logger.Info("retrying failed instrumentations", "count", len(byPid), "distroFilter", distroFilter)
	m.instrumentFromDetails(ctx, byPid)
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) handleInstrumentError(err error) {
	// ignore the error if no instrumentation factory is found,
	// as this is expected for some language and sdk combinations which don't have ebpf support.
	if errors.Is(err, errNoInstrumentationFactory) {
		return
	}

	// in cases where we detected a certain language for a container, but multiple processes are running in it,
	// only one or some of them are in the language we detected.
	if errors.Is(err, ErrProcessLanguageNotMatchesDistribution) {
		m.logger.Debug("process language does not match the detected language for container, skipping instrumentation", "err", err)
		return
	}

	// fallback to log an error
	if err != nil {
		m.logger.Error("failed to handle process exec event", "err", err)
	}
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) Run(ctx context.Context) error {
	g, errCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return m.detector.Run(errCtx)
	})

	g.Go(func() error {
		m.runEventLoop(errCtx)
		return nil
	})

	g.Go(func() error {
		// Start the FD server
		server := &unixfd.Server{
			SocketPath: unixfd.DefaultSocketPath,
			Logger:     commonlogger.ToLogr(),
			TracesFDProvider: func() int {
				return m.tracesMap.FD()
			},
			MetricsFDsProvider: func() []int {
				var fds []int
				if m.metricsMap != nil {
					fds = append(fds, m.metricsMap.FD())
				}
				if m.metricsAttributesMap != nil {
					fds = append(fds, m.metricsAttributesMap.FD())
				}
				return fds
			},
			LogsFDsProvider: func() []int {
				if m.logsMap != nil {
					return []int{m.logsMap.FD()}
				}
				return nil
			},
			LogsAttrSubscribe: m.logsAttrSubscribe,
		}

		// Run server in background to serve the map FD to relevant data collection client.
		// The server will continue running until odiglet shuts down, allowing collectors to reconnect after restarts
		// and ask for a new FD.
		if err := server.Run(errCtx); err != nil {
			m.logger.Error("unixfd server failed", "err", err)
		}

		m.logger.Info("eBPF maps created, FD server started",
			"socket", unixfd.DefaultSocketPath,
			"traces_map_fd", m.tracesMap.FD())
		return nil
	})

	err := g.Wait()

	return err
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) metricsAttributeSet(distribution *distro.OtelDistro) attribute.Set {
	return attribute.NewSet(
		semconv.TelemetryDistroName(distribution.Name),
		semconv.TelemetrySDKLanguageKey.String(string(distribution.Language)),
	)
}

// distroLoadState resolves the process's owned distribution and reports whether its distro factory
// currently has a loaded instrumentation (a non-nil entry in details.insts under the distribution name).
// distribution is the one the manager accounts the process's self metrics under; it is nil when the
// distro has no factory of its own (an unresolved distro, or one instrumented only by generic
// factories), in which case loaded is always false. The factory lookup resolves ownership, so metric
// accounting stays consistent between the increment (at instrument time) and the decrement (on exit).
func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) distroLoadState(ctx context.Context, details *instrumentationDetails[ProcessGroup, ConfigGroup, ProcessDetails]) (distribution *distro.OtelDistro, loaded bool) {
	d, err := details.pd.Distribution(ctx)
	if err != nil || d == nil {
		return nil, false
	}
	if _, hasFactory := m.factories[d.Name]; !hasFactory {
		return nil, false
	}
	// A nil entry means the distro factory was attempted but failed, so it is not loaded.
	loaded = details.insts[d.Name] != nil
	return d, loaded
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) cleanInstrumentation(ctx context.Context, pid int) {
	details, found := m.detailsByPid[pid]
	if !found {
		m.logger.Debug("no instrumentation found for exiting pid, nothing to clean", "pid", pid)
		return
	}

	m.logger.Info("cleaning instrumentation resources", "pid", pid, "process group details", details.pd)

	for _, inst := range details.insts {
		if inst == nil {
			// a factory that failed to init/load is tracked as a nil entry
			continue
		}
		if err := inst.Close(ctx); err != nil {
			m.logger.Error("failed to close instrumentation", "err", err, "pid", pid)
		}
	}

	// Decrement the gauge only for what we counted as instrumented: a loaded distro factory.
	if distribution, loaded := m.distroLoadState(ctx, details); loaded {
		m.metrics.instrumentedProcesses.Add(ctx, -1, metric.WithAttributeSet(m.metricsAttributeSet(distribution)))
	}

	// The process has exited, so delete its InstrumentationInstance. This is not gated on which
	// distro "owns" the instance: it is keyed by (pod, host pid), so it targets at most the instance
	// the manager itself would create; a native agent's instance is keyed by the pod-internal vpid,
	// a different name. In the rare hostPID case where those keys coincide, the process is already
	// gone, so cleaning up the instance is still correct.
	if err := m.handler.Reporter.OnExit(ctx, pid, details.pd); err != nil {
		m.logger.Error("failed to report instrumentation exit", "err", err)
	}

	m.stopTrackInstrumentation(pid)
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) isInstrumented(ctx context.Context, pid int) bool {
	details, found := m.detailsByPid[pid]
	if !found {
		return false
	}
	// Instrumented once the distro factory has loaded (a non-nil entry in insts). A process with no
	// factory of its own (distribution == nil, instrumented only by generic factories) has nothing to
	// retry via the distro-keyed mechanism, so it is considered instrumented and left alone.
	distribution, loaded := m.distroLoadState(ctx, details)
	return distribution == nil || loaded
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) tryInstrumentFromProcessEvent(ctx context.Context, e detector.ProcessEvent) error {
	pd, err := m.handler.ProcessDetailsResolver.Resolve(ctx, e)
	if err != nil {
		return errors.Join(err, errFailedToGetDetails)
	}

	return m.tryInstrument(ctx, pd, e.PID)
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) tryInstrument(ctx context.Context, pd ProcessDetails, pid int) error {
	if m.isInstrumented(ctx, pid) {
		// this can happen if we have multiple exec events for the same pid (chain loading)
		// TODO: better handle this?
		// this can be done by first closing the existing instrumentation,
		// and then creating a new one
		m.logger.Info("received exec event for process id which is already instrumented with ebpf, skipping it", "pid", pid, "process details", pd.String())
		return nil
	}

	otelDistro, err := pd.Distribution(ctx)
	if err != nil {
		return errors.Join(err, errFailedToGetDistribution)
	}

	configGroup, err := pd.ConfigGroup(ctx)
	if err != nil {
		return errors.Join(err, errFailedToGetConfigGroup)
	}

	processGroup, err := pd.ProcessGroup(ctx)
	if err != nil {
		return errors.Join(err, errFailedToGetProcessGroup)
	}

	// The factories that apply to this process: the distro's own factory, if it has one, plus the
	// generic factories (which apply to every process regardless of distro, e.g. OBI network
	// metrics or eBPF log capture). A process with nothing to run is ignored.
	toRun := make(map[string]Factory, len(m.genericFactories)+1)
	if factory, ok := m.factories[otelDistro.Name]; ok {
		toRun[otelDistro.Name] = factory
	}
	for name, f := range m.genericFactories {
		toRun[name] = f
	}
	if len(toRun) == 0 {
		// No factory for this distro and no generic factories. Expected for some language/sdk
		// combinations without eBPF support.
		return errNoInstrumentationFactory
	}

	// Fetch initial settings for the instrumentation (SettingsGetter interface requires logr.Logger).
	settings, err := m.handler.SettingsGetter.Settings(ctx, commonlogger.ToLogr().WithName("ebpf-instrumentation-manager"), pd, otelDistro)
	if err != nil {
		// for k8s instrumentation config CR will be queried to get the settings
		// we should always have config for this event.
		// if missing, it means that either:
		// - the config will be generated later due to reconciliation timing in instrumentor
		// - just got deleted and the pod (and the process) will go down soon
		// TODO: sync reconcilers so inst config is guaranteed be created before the webhook is enabled
		//
		m.logger.Info("failed to get initial settings for instrumentation", "language", otelDistro.Language, "distroName", otelDistro.Name, "err", err)
		// return nil
	}

	settings.TracesMap = ReaderMap{
		Map:            m.tracesMap,
		ExternalReader: true,
	}

	settings.MetricsMap = MetricsMap{
		HashMapOfMaps: m.metricsMap,
		AttributesMap: m.metricsAttributesMap,
	}

	settings.LogsMap = ReaderMap{
		Map:            m.logsMap,
		ExternalReader: true,
	}

	// Carry over entries from a previous attempt: loaded instrumentations (non-nil) keep running
	// untouched, and failures (nil) are preserved so the loop below re-runs only what is not loaded.
	// A retry thus re-runs the factories that failed before (in practice the distro factory, since
	// retries are keyed on distros - see failedProcessDetailsByDistro).
	insts := make(map[string]Instrumentation, len(toRun))
	if existing, tracked := m.detailsByPid[pid]; tracked {
		for name, inst := range existing.insts {
			insts[name] = inst
		}
	}

	var instErrs []error
	for name, f := range toRun {
		// A non-nil entry means this factory already loaded on a previous attempt; leave it running.
		// A nil (previously failed) or absent entry is (re)attempted below.
		if insts[name] != nil {
			continue
		}
		inst, initErr := f.CreateInstrumentation(ctx, pid, settings)
		if initErr != nil {
			// A failed create has no status to consult, so it always reports: only a distro is
			// expected to fail to create (a generic opts out with a nil instrumentation rather
			// than an error), so this never touches an InstrumentationInstance a generic doesn't
			// own. Track the failure as a nil entry so the pid stays a retry candidate and is closed
			// out on exit. Every factory's error - distro and generic alike - is collected and
			// returned together.
			if reporterErr := m.handler.Reporter.OnInit(ctx, pid, initErr, pd); reporterErr != nil {
				m.logger.Error("failed to report instrumentation init", "err", reporterErr, "pid", pid, "process group details", pd)
			}
			insts[name] = nil
			instErrs = append(instErrs, fmt.Errorf("initialize %q instrumentation for pid %d: %w", name, pid, initErr))
			continue
		}
		if inst == nil {
			// the factory does not apply to this process; leave it with no entry
			continue
		}

		status, loadErr := inst.Load(ctx)
		// Report the load unless the instrumentation opts out via Status.SkipReport - a generic
		// sets this so it never touches an InstrumentationInstance it doesn't own. The flag is honored
		// on a failed load too, since the status is the instrumentation's own return value.
		if !status.SkipReport {
			if reporterErr := m.handler.Reporter.OnLoad(ctx, pid, loadErr, pd, status); reporterErr != nil {
				m.logger.Error("failed to report instrumentation load", "err", reporterErr, "loaded", loadErr == nil, "pid", pid, "process group details", pd)
			}
		}
		if loadErr != nil {
			// Track the failure as a nil entry, like a failed create: the pid stays a retry
			// candidate and is closed out on exit.
			insts[name] = nil
			instErrs = append(instErrs, fmt.Errorf("load %q instrumentation for pid %d: %w", name, pid, loadErr))
			continue
		}

		insts[name] = inst

		// Run until the process exits (Close) or the manager stops (ctx is canceled). Run errors are
		// reported only for instrumentations that report their lifecycle.
		go func() {
			if err := inst.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				if !status.SkipReport {
					if reporterErr := m.handler.Reporter.OnRun(ctx, pid, err, pd); reporterErr != nil {
						m.logger.Error("failed to report instrumentation run", "err", reporterErr)
					}
				}
				m.logger.Error("failed to run instrumentation", "err", err, "pid", pid)
			}
		}()
	}

	m.startTrackInstrumentation(ctx, pid, insts, pd, processGroup, configGroup)

	if len(instErrs) > 0 {
		return errors.Join(instErrs...)
	}
	m.logger.Info("instrumentation loaded", "pid", pid, "process group details", pd)
	return nil
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) startTrackInstrumentation(
	ctx context.Context,
	pid int,
	insts map[string]Instrumentation,
	processDetails ProcessDetails,
	processGroup ProcessGroup,
	configGroup ConfigGroup,
) {
	prevDetails, hadPrev := m.detailsByPid[pid]

	instDetails := &instrumentationDetails[ProcessGroup, ConfigGroup, ProcessDetails]{
		insts: insts,
		pd:    processDetails,
		cg:    configGroup,
		pg:    processGroup,
	}
	m.detailsByPid[pid] = instDetails

	if _, found := m.detailsByConfigGroup[configGroup]; !found {
		// first instrumentation for this workload
		m.detailsByConfigGroup[configGroup] = map[int]*instrumentationDetails[ProcessGroup, ConfigGroup, ProcessDetails]{pid: instDetails}
	} else {
		m.detailsByConfigGroup[configGroup][pid] = instDetails
	}

	if _, found := m.detailsByProcessGroup[processGroup]; !found {
		// first instrumentation for this workload
		m.detailsByProcessGroup[processGroup] = map[int]*instrumentationDetails[ProcessGroup, ConfigGroup, ProcessDetails]{pid: instDetails}
	} else {
		m.detailsByProcessGroup[processGroup][pid] = instDetails
	}

	// Self metrics are attributed to the process's distribution and only tracked when its distro has
	// a factory (processes instrumented only by generic factories are not counted). Both the
	// current and previous loaded state come from the distribution's entry in insts.
	distribution, loaded := m.distroLoadState(ctx, instDetails)
	if distribution == nil {
		return
	}
	prevLoaded := false
	if hadPrev {
		_, prevLoaded = m.distroLoadState(ctx, prevDetails)
	}
	switch {
	case !loaded && !hadPrev:
		// First time the distro factory is attempted for this pid and it failed to init/load; count
		// once (failedInstrumentations is a monotonic counter).
		m.metrics.failedInstrumentations.Add(ctx, 1, metric.WithAttributeSet(m.metricsAttributeSet(distribution)))
	case loaded && !prevLoaded:
		// Transition from "not instrumented" (never seen or previously failed) to "instrumented".
		m.metrics.instrumentedProcesses.Add(ctx, 1, metric.WithAttributeSet(m.metricsAttributeSet(distribution)))
	}
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) stopTrackInstrumentation(pid int) {
	details, ok := m.detailsByPid[pid]
	if !ok {
		return
	}
	cg := details.cg
	pg := details.pg

	delete(m.detailsByPid, pid)
	delete(m.detailsByConfigGroup[cg], pid)
	delete(m.detailsByProcessGroup[pg], pid)

	if len(m.detailsByConfigGroup[cg]) == 0 {
		delete(m.detailsByConfigGroup, cg)
	}

	if len(m.detailsByProcessGroup[pg]) == 0 {
		delete(m.detailsByProcessGroup, pg)
	}
}

func (m *manager[ProcessGroup, ConfigGroup, ProcessDetails]) applyInstrumentationConfigurationForSDK(ctx context.Context, configGroup ConfigGroup, config Config) error {
	var err error

	configGroupInstrumentations, ok := m.detailsByConfigGroup[configGroup]
	if !ok {
		return nil
	}

	for _, instDetails := range configGroupInstrumentations {
		m.logger.Info("applying configuration to instrumentation", "process group details", instDetails.pd, "configGroup", configGroup)
		// Fan out to every instrumentation of the process, distro factory and generic alike
		// (e.g. so OBI network metrics can be toggled on/off via config, including for processes
		// that have no factory of their own).
		for _, inst := range instDetails.insts {
			if inst == nil {
				// a factory that failed to init/load is tracked as a nil entry
				continue
			}
			err = errors.Join(err, inst.ApplyConfig(ctx, config))
		}
	}
	return err
}
