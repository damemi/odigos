package main

import (
	"flag"
	_ "net/http/pprof"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	enterprisedistros "github.com/odigos-io/odigos-enterprise/distros"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/sdks"
	enterpriseinstrumentlang "github.com/odigos-io/odigos-enterprise/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos-enterprise/odigosauth"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet"
	"github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation"
	communityinstrumentlang "github.com/odigos-io/odigos/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
)

const (
	configUpdatesBufferSize = 10
)

func main() {
	var healthProbeBindPort int
	flag.IntVar(&healthProbeBindPort, "health-probe-bind-port", k8sconsts.OdigletDefaultHealthProbeBindPort, "The port the probe endpoint binds to.")
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

	factories, err := ebpfInstrumentationFactories()
	if err != nil {
		log.Logger.Error(err, "failed to set up ebpf factories")
		os.Exit(1)
	}

	instrumentationManagerOptions := ebpf.InstrumentationManagerOptions{
		Factories:                  factories,
		DistributionGetter:         enterpriseDistrosProvider.Getter,
		OdigletHealthProbeBindPort: healthProbeBindPort,
	}

	o, err := odiglet.New(clientset, deviceInjectionCallbacks(), instrumentationManagerOptions)
	if err != nil {
		log.Logger.Error(err, "Failed to initialize odiglet")
		os.Exit(1)
	}

	ctx := signals.SetupSignalHandler()
	o.Run(ctx)

	log.Logger.V(0).Info("odiglet exiting")
}

func deviceInjectionCallbacks() instrumentation.OtelSdksLsf {
	otelSdkEbpfEnterprise := common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier}
	otelSdkNativeCommunity := common.OtelSdk{SdkType: common.NativeOtelSdkType, SdkTier: common.CommunityOtelSdkTier}
	otelSdkNativeEnterprise := common.OtelSdk{SdkType: common.NativeOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier}

	return map[common.ProgrammingLanguage]map[common.OtelSdk]instrumentation.LangSpecificFunc{
		common.GoProgrammingLanguage: {
			otelSdkEbpfEnterprise: enterpriseinstrumentlang.EnterpriseGo,
		},
		// For Java there are currently 3 different directors:
		// 1. native-community: regular otel java agent.
		// 2. ebpf-enterprise: our custom ebpf java agent with our written instrumentations.
		// 3. native-enterprise: using the otel java agent extension mechanism, to export the spans
		//   to ebpf.
		// 2 and 3 should not be kept together for the long run and we should decide on one of them.
		// Once we decide, the implementation we use should be called ebpf-enterprise.
		// The third SDK is identical concept to the JS and Python SDKs (which are called ebpf-enterprise),
		// we called it native-enterprise for backward compatibility, and the need to use both of them if we need to.
		common.JavaProgrammingLanguage: {
			otelSdkNativeCommunity:  communityinstrumentlang.Java,
			otelSdkEbpfEnterprise:   enterpriseinstrumentlang.EnterpriseJava,
			otelSdkNativeEnterprise: enterpriseinstrumentlang.NativeEnterpriseJava,
		},
		common.PythonProgrammingLanguage: {
			otelSdkNativeCommunity: communityinstrumentlang.Python,
			otelSdkEbpfEnterprise:  enterpriseinstrumentlang.EnterprisePython,
		},
		common.JavascriptProgrammingLanguage: {
			otelSdkNativeCommunity: communityinstrumentlang.NodeJS,
			otelSdkEbpfEnterprise:  enterpriseinstrumentlang.EnterpriseNodeJS,
		},
		common.DotNetProgrammingLanguage: {
			otelSdkNativeCommunity:  communityinstrumentlang.DotNet,
			otelSdkNativeEnterprise: enterpriseinstrumentlang.LegacyDotNet,
		},
		common.MySQLProgrammingLanguage: {
			otelSdkEbpfEnterprise: enterpriseinstrumentlang.EnterpriseMySQL,
		},
	}
}

func ebpfInstrumentationFactories() (map[string]commonInstrumentation.Factory, error) {
	goFactory, err := sdks.NewGoInstrumentationFactoryEnterprise()
	if err != nil {
		return nil, err
	}

	return map[string]commonInstrumentation.Factory{
		"golang-enterprise":          goFactory,
		"java-ebpf-instrumentations": sdks.NewJavaInstrumentationFactoryEnterprise(),
		"java-enterprise":            sdks.NewJavaExtensionInstrumentationFactoryEnterprise(),
		"python-enterprise":          sdks.NewPythonInstrumentationFactoryEnterprise(),
		"nodejs-enterprise":          sdks.NewNodeJSInstrumentationFactoryEnterprise(),
		"mysql-enterprise":           sdks.NewMySQLInstrumentationFactoryEnterprise(),
		// dotnet, ruby and php are using native instrumentation and does not have ebpf instrumentation.
	}, nil
}
