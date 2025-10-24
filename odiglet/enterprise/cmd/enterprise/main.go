package main

import (
	"flag"
	_ "net/http/pprof"
	"os"
	"strconv"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	enterprisedistros "github.com/odigos-io/odigos-enterprise/distros"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/sdks"
	"github.com/odigos-io/odigos-enterprise/odigosauth"
	"github.com/odigos-io/odigos/api/k8sconsts"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/odiglet"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
)

const (
	configUpdatesBufferSize = 10
)

func main() {
	var healthProbeBindPort int
	var traceIdSuffix string
	flag.IntVar(&healthProbeBindPort, "health-probe-bind-port", k8sconsts.OdigletDefaultHealthProbeBindPort, "The port the probe endpoint binds to.")
	flag.StringVar(&traceIdSuffix, "trace-id-suffix", "", "Hex value for the unique identifier for timestamp-based trace IDs. Must be 1 byte. Example: 5E.")
	flag.Parse()

	// Init Kubernetes clientset
	cfg, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}

	// If started in init mode
	if len(os.Args) == 2 && os.Args[1] == "init" {
		odiglet.OdigletInitPhase(clientset)
	}

	if err := log.Init(); err != nil {
		panic(err)
	}
	log.Logger.V(0).Info("Starting odiglet enterprise")

	odigosauth.AssertEnterpriseOdigosToken(log.Logger)

	if err := env.Load(); err != nil {
		log.Logger.Error(err, "Failed to load env")
		os.Exit(1)
	}

	enterpriseDistrosProvider, err := enterprisedistros.NewEnterpriseProvider()
	if err != nil {
		log.Logger.Error(err, "Failed to create enterprise distros getter")
		os.Exit(1)
	}

	suffix := uint64(0)
	if traceIdSuffix != "" {
		suffix, err = strconv.ParseUint(traceIdSuffix, 16, 8)
		if err != nil {
			log.Logger.Error(err, "failed to parse TRACE_ID_SUFFIX")
			os.Exit(1)
		}
	}

	factories, err := ebpfInstrumentationFactories(uint8(suffix))
	if err != nil {
		log.Logger.Error(err, "failed to set up ebpf factories")
		os.Exit(1)
	}

	instrumentationManagerOptions := ebpf.InstrumentationManagerOptions{
		Factories:                  factories,
		DistributionGetter:         enterpriseDistrosProvider.Getter,
		OdigletHealthProbeBindPort: healthProbeBindPort,
	}

	o, err := odiglet.New(clientset, instrumentationManagerOptions)
	if err != nil {
		log.Logger.Error(err, "Failed to initialize odiglet")
		os.Exit(1)
	}

	ctx := signals.SetupSignalHandler()
	o.Run(ctx)

	log.Logger.V(0).Info("odiglet exiting")
}

func ebpfInstrumentationFactories(suffix uint8) (map[string]commonInstrumentation.Factory, error) {
	goFactory, err := sdks.NewGoInstrumentationFactoryEnterprise(suffix)
	if err != nil {
		return nil, err
	}

	return map[string]commonInstrumentation.Factory{
		"golang-enterprise":          goFactory,
		"java-ebpf-instrumentations": sdks.NewJavaInstrumentationFactoryEnterprise(suffix),
		"java-enterprise":            sdks.NewJavaExtensionInstrumentationFactoryEnterprise(),
		"python-enterprise":          sdks.NewPythonInstrumentationFactoryEnterprise(),
		"nodejs-enterprise":          sdks.NewNodeJSInstrumentationFactoryEnterprise(),
		"mysql-enterprise":           sdks.NewMySQLInstrumentationFactoryEnterprise(),
		// dotnet, ruby and php are using native instrumentation and does not have ebpf instrumentation.
	}, nil
}
