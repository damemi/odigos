package main

import (
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation/fs"

	"net/http"
	_ "net/http/pprof"

	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf"
	enterpriseinstrumentlang "github.com/odigos-io/odigos-enterprise/odiglet/pkg/instrumentation/instrumentlang"
	enterprisekube "github.com/odigos-io/odigos-enterprise/odiglet/pkg/kube"
	"github.com/odigos-io/odigos/common"
	k8senv "github.com/odigos-io/odigos/k8sutils/pkg/env"
	odigosebpf "github.com/odigos-io/odigos/odiglet/pkg/ebpf"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation"
	communityinstrumentlang "github.com/odigos-io/odigos/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos/odiglet/pkg/kube"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
	"github.com/odigos-io/odigos/opampserver/pkg/server"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
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

	odigosNs := k8senv.GetCurrentNamespace()
	go func() {
		err = server.StartOpAmpServer(ctx, log.Logger, mgr, clientset, env.Current.NodeName, odigosNs)
		if err != nil {
			log.Logger.Error(err, "Failed to start opamp server")
		}
	}()

	ebpfDirectors, err := initEbpf(ctx, mgr.GetClient(), mgr.GetScheme())
	if err != nil {
		log.Logger.Error(err, "Failed to init eBPF director")
		os.Exit(-1)
	}

	plainDirectors := make(odigosebpf.DirectorsMap)
	for k, v := range ebpfDirectors {
		plainDirectors[k] = v
	}

	// odigos reconcilers
	err = kube.SetupWithManager(mgr, plainDirectors, clientset)
	if err != nil {
		log.Logger.Error(err, "Failed to setup controller-runtime manager")
		os.Exit(-1)
	}

	// enterprise reconcilers
	if err = (&enterprisekube.InstrumentationConfigReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Directors: ebpfDirectors,
	}).SetupWithManager(mgr); err != nil {
		log.Logger.Error(err, "unable to create controller", "controller", "InstrumentationConfig")
		os.Exit(1)
	}

	go func() {
		err := mgr.Start(ctx)
		if err != nil {
			log.Logger.Error(err, "Failed to start controller-runtime manager")
			os.Exit(-1)
		}
	}()

	<-ctx.Done()
	for _, director := range ebpfDirectors {
		director.Shutdown()
	}
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

func initEbpf(ctx context.Context, kubeclient client.Client, scheme *runtime.Scheme) (ebpf.DirectorsEnterpriseMap, error) {
	goInstrumentationFactory := ebpf.NewGoInstrumentationFactoryEnterprise(kubeclient)
	goDirector := ebpf.NewEbpfDirectorEnterprise(ctx, kubeclient, scheme, common.GoProgrammingLanguage, goInstrumentationFactory)
	goDirectorKey := odigosebpf.DirectorKey{
		Language: common.GoProgrammingLanguage,
		OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
	}

	javaInstrumentationFactory := ebpf.NewJavaInstrumentationFactoryEnterprise(kubeclient)
	javaDirector := ebpf.NewEbpfDirectorEnterprise(ctx, kubeclient, scheme, common.JavaProgrammingLanguage, javaInstrumentationFactory)
	javaDirectorKey := odigosebpf.DirectorKey{
		Language: common.JavaProgrammingLanguage,
		OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
	}

	// Utilizing the Otel Java Agent extension mechanism
	// We define it as a native-enterprise director but it also requires ebpf initialization.
	// TODO: this is a similar implementation to what we have for JS and Python,
	// so maybe should consider defining them as native-enterprise as well.
	// On the other hand, if we'll choose to use this instrumentation without keeping the original ebpf-enterprise for Java,
	// we might consider defining this one as ebpf-enterprise instead of the old one.
	// Currently we can't define this as ebpf-enterprise because it will conflict with the old Java ebpf director.
	javaExtensionInstrumentationFactory := ebpf.NewJavaExtensionInstrumentationFactoryEnterprise(kubeclient)
	javaExtensionDirector := ebpf.NewEbpfDirectorEnterprise(ctx, kubeclient, scheme, common.JavaProgrammingLanguage, javaExtensionInstrumentationFactory)
	javaExtensionDirectorKey := odigosebpf.DirectorKey{
		Language: common.JavaProgrammingLanguage,
		OtelSdk:  common.OtelSdk{SdkType: common.NativeOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
	}

	pythonInstrumentationFactory := ebpf.NewPythonInstrumentationFactoryEnterprise(kubeclient)
	pythonDirector := ebpf.NewEbpfDirectorEnterprise(ctx, kubeclient, scheme, common.PythonProgrammingLanguage, pythonInstrumentationFactory)
	pythonDirectorKey := odigosebpf.DirectorKey{
		Language: common.PythonProgrammingLanguage,
		OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
	}

	nodeJSInstrumentationFactory := ebpf.NewNodeJSInstrumentationFactoryEnterprise(kubeclient)
	nodeJSDirector := ebpf.NewEbpfDirectorEnterprise(ctx, kubeclient, scheme, common.JavascriptProgrammingLanguage, nodeJSInstrumentationFactory)
	nodeJSDirectorKey := odigosebpf.DirectorKey{
		Language: common.JavascriptProgrammingLanguage,
		OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
	}

	mySQLInstrumentationFactory := ebpf.NewMySQLInstrumentationFactoryEnterprise(kubeclient)
	mySQLDirector := ebpf.NewEbpfDirectorEnterprise(ctx, kubeclient, scheme, common.MySQLProgrammingLanguage, mySQLInstrumentationFactory)
	mySQLDirectorKey := odigosebpf.DirectorKey{
		Language: common.MySQLProgrammingLanguage,
		OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
	}

	return ebpf.DirectorsEnterpriseMap{
		goDirectorKey:            goDirector,
		javaDirectorKey:          javaDirector,
		javaExtensionDirectorKey: javaExtensionDirector,
		pythonDirectorKey:        pythonDirector,
		nodeJSDirectorKey:        nodeJSDirector,
		mySQLDirectorKey:         mySQLDirector,
	}, nil
}

func startPprofServer(logger logr.Logger) {
	logger.Info("Starting pprof server")
	addr := fmt.Sprintf(":%d", consts.PprofOdigosPort)
	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Error(err, "unable to start pprof server")
	}
}
