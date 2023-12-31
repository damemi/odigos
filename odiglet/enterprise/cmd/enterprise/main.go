package main

import (
	"context"
	"os"

	"github.com/keyval-dev/odigos-enterprise/odiglet/pkg/ebpf"
	enterpriseinstrumentlang "github.com/keyval-dev/odigos-enterprise/odiglet/pkg/instrumentation/instrumentlang"
	enterprisekube "github.com/keyval-dev/odigos-enterprise/odiglet/pkg/kube"
	"github.com/keyval-dev/odigos/common"
	odigosebpf "github.com/keyval-dev/odigos/odiglet/pkg/ebpf"
	"github.com/keyval-dev/odigos/odiglet/pkg/env"
	"github.com/keyval-dev/odigos/odiglet/pkg/instrumentation"
	communityinstrumentlang "github.com/keyval-dev/odigos/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/keyval-dev/odigos/odiglet/pkg/kube"
	"github.com/keyval-dev/odigos/odiglet/pkg/log"
	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

func main() {
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
	
	ebpfDirectors, err := initEbpf(mgr.GetClient())
	if err != nil {
		log.Logger.Error(err, "Failed to init eBPF director")
		os.Exit(-1)
	}

	plainDirectors := make(map[common.ProgrammingLanguage]odigosebpf.Director)
	for k, v := range ebpfDirectors {
		plainDirectors[k] = v
	}

	// odigos reconcilers
	err = kube.SetupWithManager(mgr, plainDirectors)
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

	ctx := signals.SetupSignalHandler()
	err = kube.StartManager(ctx, mgr)
	if err != nil {
		log.Logger.Error(err, "Failed to start controller-runtime manager")
		os.Exit(-1)
	}

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

	otelSdkLsf := map[common.ProgrammingLanguage]map[common.OtelSdk]instrumentation.LangSpecificFunc{
		common.GoProgrammingLanguage: {
			otelSdkEbpfEnterprise: enterpriseinstrumentlang.EnterpriseGo,
		},
		common.JavaProgrammingLanguage: {
			otelSdkNativeCommunity: communityinstrumentlang.Java,
			otelSdkEbpfEnterprise:  enterpriseinstrumentlang.EnterpriseJava,
		},
		common.PythonProgrammingLanguage: {
			otelSdkNativeCommunity: communityinstrumentlang.Python,
		},
		common.JavascriptProgrammingLanguage: {
			otelSdkNativeCommunity: communityinstrumentlang.NodeJS,
		},
		common.DotNetProgrammingLanguage: {
			otelSdkNativeCommunity: communityinstrumentlang.DotNet,
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

func initEbpf(kubeclient client.Client) (map[common.ProgrammingLanguage]ebpf.DirectorEnterprise, error) {
	goInstrumentationFactory := ebpf.NewGoInstrumentationFactoryEnterprise(kubeclient)
	goDirector := ebpf.NewEbpfDirectorEnterprise(common.GoProgrammingLanguage, goInstrumentationFactory)

	javaInstrumentationFactory := ebpf.NewJavaInstrumentationFactoryEnterprise(kubeclient)
	javaDirector := ebpf.NewEbpfDirectorEnterprise(common.JavaProgrammingLanguage, javaInstrumentationFactory)

	return map[common.ProgrammingLanguage]ebpf.DirectorEnterprise{
		common.GoProgrammingLanguage:   goDirector,
		common.JavaProgrammingLanguage: javaDirector,
	}, nil
}
