package odiglet

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/odigos-io/ebpf-core/pkg/instrumentors/kernel"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/fs"
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/distros"
	"github.com/odigos-io/odigos/distros/distro"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	criwrapper "github.com/odigos-io/odigos/k8sutils/pkg/cri"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/k8sutils/pkg/feature"
	"github.com/odigos-io/odigos/k8sutils/pkg/metrics"
	k8snode "github.com/odigos-io/odigos/k8sutils/pkg/node"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf/sdks"
	obisdk "github.com/odigos-io/odigos/odiglet/pkg/ebpf/sdks/obi"
	"github.com/odigos-io/odigos/odiglet/pkg/kube"
	ebpfMetrics "github.com/odigos-io/odigos/odiglet/pkg/metrics"
	"github.com/odigos-io/odigos/odiglet/pkg/process"
	"github.com/odigos-io/odigos/opampserver/pkg/server"
	"golang.org/x/sync/errgroup"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"k8s.io/client-go/kubernetes"
	controllerruntime "sigs.k8s.io/controller-runtime"
)

// runnable is a named task started in Odiglet.Run's errgroup. Run should block until ctx is
// canceled. Return nil on normal shutdown. When PropagateErr is true, a non-nil error is
// returned to the errgroup and cancels the other runnables.
type runnable struct {
	Name         string
	PropagateErr bool
	Run          func(ctx context.Context) error
}

type Odiglet struct {
	clientset               *kubernetes.Clientset
	mgr                     controllerruntime.Manager
	ebpfManager             commonInstrumentation.Manager
	configUpdates           chan<- commonInstrumentation.ConfigUpdate[ebpf.K8sConfigGroup]
	instrumentationRequests chan commonInstrumentation.Request[ebpf.K8sProcessGroup, ebpf.K8sConfigGroup, *ebpf.K8sProcessDetails]
	criClient               *criwrapper.CriClient
	obiManager              *obisdk.Manager
	maps                    *ebpfMapExchange
	exporters               *exporters
	kernelObservability     *kernel.Instrumentation
	logger                  *commonlogger.OdigosLogger
}

// channel sizes for sending events to the instrumentation manager's event loop.
// during bursts, or start-ups we want to be able to queue events in the channels without blocking the reconciler.
const (
	configUpdatesBufferSize           = 100
	instrumentationRequestsBufferSize = 200
)

// Config holds what odiglet is configured with at startup.
type Config struct {
	HealthProbeBindPort int
	DistributionGetter  *distros.Getter
	TraceIDSuffix       uint8
	WaspDisabled        bool
}

// New creates a new Odiglet instance. On failure, it releases the shared resources it already created.
func New(ctx context.Context, clientset *kubernetes.Clientset, cfg Config) (_ *Odiglet, err error) {
	o := &Odiglet{
		clientset: clientset,
		logger:    commonlogger.LoggerCompat().With("subsystem", "eventloop"),
	}
	defer func() {
		if err != nil {
			o.closeShared()
		}
	}()

	// The eBPF SDKs write into shared maps, served over the unix socket to the
	// collector's odigosebpfreceiver.
	o.maps, err = newEbpfMapExchange()
	if err != nil {
		return nil, fmt.Errorf("failed to set up the ebpf map exchange: %w", err)
	}

	if err = feature.Setup(); err != nil {
		return nil, err
	}
	process.DiscoverCgroupLayout()

	if o.mgr, err = kube.CreateManager(cfg.HealthProbeBindPort); err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime manager %w", err)
	}
	if err = setupMetrics(); err != nil {
		return nil, err
	}
	appendEnvVarNames := distro.GetAppendEnvVarNames(cfg.DistributionGetter.GetAllDistros())
	if err = o.setupEBPFManager(ctx, cfg, appendEnvVarNames); err != nil {
		return nil, err
	}

	o.criClient = &criwrapper.CriClient{Logger: commonlogger.ToLogr()}
	err = kube.SetupWithManager(kube.KubeManagerOptions{
		Mgr:                     o.mgr,
		ConfigUpdates:           o.configUpdates,
		InstrumentationRequests: o.instrumentationRequests,
		CriClient:               o.criClient,
		AppendEnvVarNames:       appendEnvVarNames,
	}, cfg.DistributionGetter)
	if err != nil {
		return nil, fmt.Errorf("failed to setup controller-runtime manager %w", err)
	}
	return o, nil
}

// setupMetrics registers an OpenTelemetry MeterProvider based on the controller-runtime prometheus
// registry as the global MeterProvider, and the eBPF metrics collector on top of it.
func setupMetrics() error {
	provider, err := metrics.NewMeterProviderForController(resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.K8SNodeName(env.Current.NodeName),
	))
	if err != nil {
		return fmt.Errorf("failed to create OpenTelemetry MeterProvider: %w", err)
	}
	otel.SetMeterProvider(provider)

	metricsLogger := commonlogger.LoggerCompat().With("subsystem", "ebpfmetrics")
	collector := ebpfMetrics.NewEBPFMetricsCollector(metricsLogger)
	if err := collector.RegisterMetrics(); err != nil {
		metricsLogger.Error("failed to register metrics", "err", err)
	}
	return nil
}

// setupEBPFManager creates the shared exporters, the kernel instrumentation, every instrumentation
// factory, and the eBPF instrumentation manager built from them with the channels the kube
// controllers drive it through.
func (o *Odiglet) setupEBPFManager(ctx context.Context, cfg Config, appendEnvVarNames map[string]struct{}) error {
	logger := commonlogger.LoggerCompat().With("subsystem", "startup")
	var err error
	o.exporters, err = newExporters(ctx, logger)
	if err != nil {
		return err
	}

	// No eBPF object is loaded here: the kernel instrumentation loads its programs and maps when the
	// first process whose workload enables kernel traces is loaded by its generic factory.
	o.kernelObservability, err = kernel.NewInstrumentation(o.exporters.traces, kernel.WithLogger(slog.New(commonlogger.NewSlogHandler("kernelobservability"))))
	if err != nil {
		return fmt.Errorf("failed to create the kernel instrumentation: %w", err)
	}

	factories, genericFactories, obiManager, err := instrumentationFactories(cfg, o.exporters, o.kernelObservability, logger)
	if err != nil {
		return fmt.Errorf("failed to set up ebpf factories: %w", err)
	}
	o.obiManager = obiManager

	configUpdates := make(chan commonInstrumentation.ConfigUpdate[ebpf.K8sConfigGroup], configUpdatesBufferSize)
	instrumentationRequests := make(chan commonInstrumentation.Request[ebpf.K8sProcessGroup, ebpf.K8sConfigGroup, *ebpf.K8sProcessDetails], instrumentationRequestsBufferSize)

	opts := ebpf.InstrumentationManagerOptions{
		Factories:          factories,
		GenericFactories:   genericFactories,
		DistributionGetter: cfg.DistributionGetter,
		TracesMap:          o.maps.traces,
		LogsMap:            o.maps.logs,
	}
	ebpfLogger := commonlogger.LoggerCompat().With("subsystem", "ebpfmanager")
	o.ebpfManager, err = ebpf.NewManager(o.mgr.GetClient(), ebpfLogger, opts, configUpdates, instrumentationRequests, appendEnvVarNames)
	if err != nil {
		return fmt.Errorf("failed to create ebpf manager %w", err)
	}

	o.configUpdates = configUpdates
	o.instrumentationRequests = instrumentationRequests
	return nil
}

// Run starts the Odiglet components and blocks until the context is cancelled, or a critical error occurs.
func (o *Odiglet) Run(ctx context.Context) {
	// Runs after every runnable has exited, so no instrumentation is still exporting.
	defer o.closeShared()

	if err := o.criClient.Connect(ctx); err != nil {
		o.logger.Error("Failed to connect to CRI runtime", "err", err)
	}
	defer o.criClient.Close()

	g, groupCtx := errgroup.WithContext(ctx)
	for _, r := range o.runnables() {
		g.Go(func() error {
			err := r.Run(groupCtx)
			if err != nil {
				o.logger.Error("failed to run "+r.Name, "err", err)
			} else {
				o.logger.Info(r.Name + " exited")
			}
			if r.PropagateErr {
				return err
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		o.logger.Error("Odiglet exited with error", "err", err)
	}
}

func (o *Odiglet) runnables() []runnable {
	odigosNs := env.GetCurrentNamespace()
	ebpfDone := make(chan struct{})
	return []runnable{
		{
			Name: "pprof server",
			// if we fail to start the pprof server, don't return an error as it is not critical
			PropagateErr: false,
			Run: func(ctx context.Context) error {
				return common.StartPprofServer(ctx, commonlogger.ToLogr(), int(k8sconsts.DefaultPprofEndpointPort))
			},
		},
		{
			Name:         "eBPF manager",
			PropagateErr: true,
			Run: func(ctx context.Context) error {
				defer close(ebpfDone)
				return o.ebpfManager.Run(ctx)
			},
		},
		{
			Name:         "OpAmp server",
			PropagateErr: true,
			Run: func(ctx context.Context) error {
				return server.StartOpAmpServer(ctx, o.mgr, o.clientset, env.Current.NodeName, odigosNs)
			},
		},
		{
			Name:         "kube manager",
			PropagateErr: true,
			Run: func(ctx context.Context) error {
				// Create a context that will be cancelled when eBPF manager exits during shutdown, as the
				// eBPF manager still deletes its InstrumentationInstances through the kube manager's client.
				kubeManagerCtx, kubeManagerCancel := context.WithCancel(context.Background())
				defer kubeManagerCancel()

				go func() {
					select {
					case <-ctx.Done():
						o.logger.Info("Shutdown initiated, waiting for eBPF manager to exit before stopping kube manager")
						<-ebpfDone
						o.logger.Info("eBPF manager exited, now stopping kube manager")
						kubeManagerCancel()
					case <-kubeManagerCtx.Done():
						return
					}
				}()

				err := o.mgr.Start(kubeManagerCtx)
				if o.configUpdates != nil {
					close(o.configUpdates)
				}
				// We don't close instrumentationRequests here: the manager's runEventLoop returns on
				// ctx.Done() so it doesn't need a channel close to terminate, and closing would race
				// with its other producers (the Go offsets watcher, or kube reconcilers mid-send).
				return err
			},
		},
		{
			Name:         "OBI manager",
			PropagateErr: false,
			Run:          o.obiManager.Run,
		},
		{
			Name:         "ebpf map fd server",
			PropagateErr: false,
			Run:          o.maps.serve,
		},
		{
			Name:         "go offsets watcher",
			PropagateErr: true,
			Run: func(ctx context.Context) error {
				return sdks.WatchGoOffsets(ctx, o.instrumentationRequests, commonlogger.LoggerCompat().With("subsystem", "ebpfgosdk"))
			},
		},
	}
}

// closeShared releases what all instrumentations share, once none of them is running. The kernel
// instrumentation exports through the traces exporter, so it is closed before the exporters are
// drained. Fields that were never set are skipped, so it is safe on a partially initialized odiglet.
func (o *Odiglet) closeShared() {
	if o.maps != nil {
		o.maps.close()
	}
	if o.kernelObservability != nil {
		if err := o.kernelObservability.Close(); err != nil {
			o.logger.Error("failed to close the kernel instrumentation", "error", err)
		}
	}
	if o.exporters != nil {
		o.exporters.close(o.logger)
	}
}

// InitPhase prepares the node before odiglet starts: it copies the agents directory to the host and
// labels the node. It runs in odiglet's init container.
func InitPhase(clientset *kubernetes.Clientset, logger *commonlogger.OdigosLogger) error {
	start := time.Now()

	// Runs before the copy because new files inherit their directory's type: on a
	// fresh install rsync then writes everything already labeled, and on an upgrade
	// only files predating this run need fixing. Opt-in because OpenShift is the
	// only platform known to need it.
	if os.Getenv(k8sconsts.OpenShiftEnabledEnvVar) == "true" {
		relabeled, rootType, err := fs.ApplyOpenShiftSELinuxSettings(k8sconsts.OdigosAgentsDirectory)
		if err != nil {
			return fmt.Errorf("failed to label agent files for SELinux: %w", err)
		}
		logger.Info("Labeled agent files for SELinux", "dir", k8sconsts.OdigosAgentsDirectory, "relabeled", relabeled, "rootType", rootType)
	}

	err := fs.CopyAgentsDirectoryToHost(k8sconsts.OdigletContainerAgentDirectory, k8sconsts.OdigosAgentsDirectory, nil)
	if err != nil {
		return fmt.Errorf("failed to copy agents directory to host: %w", err)
	}

	nn, ok := os.LookupEnv(k8sconsts.NodeNameEnvVar)
	if !ok {
		return fmt.Errorf("failed to load env: env var %s is not set", k8sconsts.NodeNameEnvVar)
	}

	if k8snode.IsGKEManagedNode(nn) {
		logger.Info("Skipping node label setup for GKE Autopilot")
	} else {
		if err := k8snode.PrepareNodeForOdigosInstallation(clientset, nn); err != nil {
			return fmt.Errorf("failed to prepare node for Odigos installation: %w", err)
		}
		logger.Info("Successfully prepared node for Odigos installation")
	}

	logger.Info("Odiglet init phase finished", "duration", time.Since(start))
	return nil
}
