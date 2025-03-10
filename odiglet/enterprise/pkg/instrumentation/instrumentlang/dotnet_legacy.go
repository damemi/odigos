package instrumentlang

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/service"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	enableProfilingEnvVar = "CORECLR_ENABLE_PROFILING"
	profilerEndVar        = "CORECLR_PROFILER"
	profilerId            = "{918728DD-259F-4A6A-AC2B-B85E1B658318}"
	profilerPathEnv       = "CORECLR_PROFILER_PATH"
	profilerPath          = "/var/odigos/legacy-dotnet/OpenTelemetry.AutoInstrumentation.ClrProfiler.Native.so"
	serviceNameEnv        = "OTEL_SERVICE_NAME"
	collectorUrlEnv       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	tracerHomeEnv         = "OTEL_DOTNET_AUTO_HOME"
	exportTypeEnv         = "OTEL_TRACES_EXPORTER"
	tracerHome            = "/var/odigos/legacy-dotnet"
	startupHookEnv        = "DOTNET_STARTUP_HOOKS"
	startupHook           = "/var/odigos/legacy-dotnet/netcoreapp3.1/OpenTelemetry.AutoInstrumentation.StartupHook.dll"
	additonalDepsEnv      = "DOTNET_ADDITIONAL_DEPS"
	additonalDeps         = "/var/odigos/legacy-dotnet/AdditionalDeps"
	sharedStoreEnv        = "DOTNET_SHARED_STORE"
	sharedStore           = "/var/odigos/legacy-dotnet/store"
)

func LegacyDotNet(deviceId string, uniqueDestinationSignals map[common.ObservabilitySignal]struct{}) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			enableProfilingEnvVar: "1",
			profilerEndVar:        profilerId,
			profilerPathEnv:       profilerPath,
			tracerHomeEnv:         tracerHome,
			collectorUrlEnv:       service.LocalTrafficOTLPHttpDataCollectionEndpoint(env.Current.NodeIP),
			serviceNameEnv:        deviceId,
			exportTypeEnv:         "otlp",
			startupHookEnv:        startupHook,
			additonalDepsEnv:      additonalDeps,
			sharedStoreEnv:        sharedStore,
		},
		Mounts: []*v1beta1.Mount{
			{
				ContainerPath: "/var/odigos/legacy-dotnet",
				HostPath:      "/var/odigos/legacy-dotnet",
				ReadOnly:      true,
			},
		},
	}
}
