package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/go-logr/logr"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation/fs"

	"net/http"
	_ "net/http/pprof"

	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/sdks"
	enterpriseinstrumentlang "github.com/odigos-io/odigos-enterprise/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos/common"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	k8senv "github.com/odigos-io/odigos/k8sutils/pkg/env"
	odigosebpf "github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation"
	communityinstrumentlang "github.com/odigos-io/odigos/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos/odiglet/pkg/kube"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"github.com/odigos-io/odigos/opampserver/pkg/server"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

const (
	configUpdatesBufferSize = 10
)

func odigletInitPhase() {
	if err := log.Init(); err != nil {
		panic(err)
	}
	err := fs.CopyAgentsDirectoryToHost()
	if err != nil {
		log.Logger.Error(err, "Failed to copy agents directory to host")
		os.Exit(-1)
	}
	os.Exit(0)
}

func main() {
	// If started in init mode
	if len(os.Args) == 2 && os.Args[1] == "init" {
		odigletInitPhase()
	}

	if err := log.Init(); err != nil {
		panic(err)
	}
	log.Logger.V(0).Info("Starting odiglet enterprise")

	assertProOdigletToken()

	// TODO; from this point the code is identical to the OSS version, except the ebpf factories amd the devices
	// We should use here a common function from the OSS repo which have the factories and the devices as parameters
	// Load env
	if err := env.Load(); err != nil {
		log.Logger.Error(err, "Failed to load env")
		os.Exit(1)
	}

	// Init Kubernetes API client
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Logger.Error(err, "Failed to init Kubernetes API client")
		os.Exit(-1)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Logger.Error(err, "Failed to init Kubernetes API client")
		os.Exit(-1)
	}

	go startDeviceManager(clientset)

	mgr, err := kube.CreateManager()
	if err != nil {
		log.Logger.Error(err, "Failed to create controller-runtime manager")
		os.Exit(-1)
	}

	ctx := signals.SetupSignalHandler()

	go startPprofServer(log.Logger)

	var wg sync.WaitGroup
	odigosNs := k8senv.GetCurrentNamespace()
	wg.Add(1)
	go func() {
		defer wg.Done()
		err = server.StartOpAmpServer(ctx, log.Logger, mgr, clientset, env.Current.NodeName, odigosNs)
		if err != nil {
			log.Logger.Error(err, "Failed to start opamp server")
		}
	}()

	configUpdates := make(chan commonInstrumentation.ConfigUpdate[odigosebpf.K8sConfigGroup], configUpdatesBufferSize)
	ebpfManager, err := odigosebpf.NewManager(
		mgr.GetClient(),
		log.Logger,
		ebpfInstrumentationFactories(),
		configUpdates,
	)
	if err != nil {
		log.Logger.Error(err, "Failed to create ebpf manager")
		os.Exit(-1)
	}

	wg.Add(1)
	go func () {
		defer wg.Done()
		err = ebpfManager.Run(ctx)
		if err != nil {
			log.Logger.Error(err, "Failed to run ebpf manager")
		}
		log.Logger.V(0).Info("eBPF manager exited")
	}()

	// odigos reconcilers
	err = kube.SetupWithManager(mgr, nil, clientset, configUpdates)
	if err != nil {
		log.Logger.Error(err, "Failed to setup controller-runtime manager")
		os.Exit(-1)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := mgr.Start(ctx)
		if err != nil {
			log.Logger.Error(err, "Failed to start controller-runtime manager")
			os.Exit(-1)
		}
		// the manager is stopped, it is now safe to close the config updates channel
		if configUpdates != nil {
			close(configUpdates)
		}
	}()

	<-ctx.Done()
	wg.Wait()
	log.Logger.V(0).Info("odiglet exiting")
}

func startDeviceManager(clientset *kubernetes.Clientset) {
	log.Logger.V(0).Info("Starting device manager")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	otelSdkEbpfEnterprise := common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier}
	otelSdkNativeCommunity := common.OtelSdk{SdkType: common.NativeOtelSdkType, SdkTier: common.CommunityOtelSdkTier}
	otelSdkNativeEnterprise := common.OtelSdk{SdkType: common.NativeOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier}

	otelSdkLsf := map[common.ProgrammingLanguage]map[common.OtelSdk]instrumentation.LangSpecificFunc{
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
			otelSdkNativeCommunity: communityinstrumentlang.DotNet,
		},
		common.MySQLProgrammingLanguage: {
			otelSdkEbpfEnterprise: enterpriseinstrumentlang.EnterpriseMySQL,
		},
	}

	lister, err := instrumentation.NewLister(ctx, clientset, otelSdkLsf)
	if err != nil {
		log.Logger.Error(err, "Failed to create new enterprise lister")
		os.Exit(-1)
	}

	manager := dpm.NewManager(lister)
	manager.Run()
}

func ebpfInstrumentationFactories() map[commonInstrumentation.OtelDistribution]commonInstrumentation.Factory {
	return map[commonInstrumentation.OtelDistribution]commonInstrumentation.Factory{
		{
			Language: common.GoProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		} : sdks.NewGoInstrumentationFactoryEnterprise(),
		{
			Language: common.JavaProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		} : sdks.NewJavaInstrumentationFactoryEnterprise(),
		// Utilizing the Otel Java Agent extension mechanism
		// We define it as a native-enterprise SDK but it also requires ebpf initialization.
		// TODO: this is a similar implementation to what we have for JS and Python,
		// so maybe should consider defining them as native-enterprise as well.
		// On the other hand, if we'll choose to use this instrumentation without keeping the original ebpf-enterprise for Java,
		// we might consider defining this one as ebpf-enterprise instead of the old one.
		// Currently we can't define this as ebpf-enterprise because it will conflict with the old Java ebpf instrumentation.
		{
			Language: common.JavaProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.NativeOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		} : sdks.NewJavaExtensionInstrumentationFactoryEnterprise(),
		{
			Language: common.PythonProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		} : sdks.NewPythonInstrumentationFactoryEnterprise(),
		{
			Language: common.JavascriptProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		} : sdks.NewNodeJSInstrumentationFactoryEnterprise(),
		{
			Language: common.MySQLProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		} : sdks.NewMySQLInstrumentationFactoryEnterprise(),
	}
}

func startPprofServer(logger logr.Logger) {
	logger.Info("Starting pprof server")
	addr := fmt.Sprintf(":%d", consts.PprofOdigosPort)
	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Error(err, "unable to start pprof server")
	}
}
