package main

import (
	"os"

	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation/fs"

	_ "net/http/pprof"

	"github.com/odigos-io/odigos-enterprise/odiglet/pkg/ebpf/sdks"
	enterpriseinstrumentlang "github.com/odigos-io/odigos-enterprise/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos/common"
	commonInstrumentation "github.com/odigos-io/odigos/instrumentation"
	"github.com/odigos-io/odigos/odiglet"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation"
	communityinstrumentlang "github.com/odigos-io/odigos/odiglet/pkg/instrumentation/instrumentlang"
	"github.com/odigos-io/odigos/odiglet/pkg/log"
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

	if err := env.Load(); err != nil {
		log.Logger.Error(err, "Failed to load env")
		os.Exit(1)
	}

	o, err := odiglet.New(deviceInjectionCallbacks(), ebpfInstrumentationFactories())
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

func ebpfInstrumentationFactories() map[commonInstrumentation.OtelDistribution]commonInstrumentation.Factory {
	return map[commonInstrumentation.OtelDistribution]commonInstrumentation.Factory{
		{
			Language: common.GoProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		}: sdks.NewGoInstrumentationFactoryEnterprise(),
		{
			Language: common.JavaProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		}: sdks.NewJavaInstrumentationFactoryEnterprise(),
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
		}: sdks.NewJavaExtensionInstrumentationFactoryEnterprise(),
		{
			Language: common.PythonProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		}: sdks.NewPythonInstrumentationFactoryEnterprise(),
		{
			Language: common.JavascriptProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		}: sdks.NewNodeJSInstrumentationFactoryEnterprise(),
		{
			Language: common.MySQLProgrammingLanguage,
			OtelSdk:  common.OtelSdk{SdkType: common.EbpfOtelSdkType, SdkTier: common.EnterpriseOtelSdkTier},
		}: sdks.NewMySQLInstrumentationFactoryEnterprise(),
	}
}
