package instrumentlang

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/envOverwrite"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	javaOtelLogsExporterEnvVar             = "OTEL_LOGS_EXPORTER"
	javaOtelTracesExporter                 = "OTEL_TRACES_EXPORTER"
	javaOtelMetricsExporter                = "OTEL_METRICS_EXPORTER"
	javaToolOptionsPatternNativeEnterprise = "-javaagent:/var/odigos/java-ext-ebpf/javaagent.jar " +
		"-Dotel.javaagent.extensions=/var/odigos/java-ext-ebpf/otel_agent_extension.jar"
)

func NativeEnterpriseJava(deviceId string, uniqueDestinationSignals map[common.ObservabilitySignal]struct{}) *v1beta1.ContainerAllocateResponse {
	javaOptionsVal, _ := envOverwrite.ValToAppend(javaToolOptionsEnvVar, common.OtelSdkNativeEnterprise)
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			javaToolOptionsEnvVar: javaOptionsVal,
			javaOptsEnvVar:        javaOptionsVal,
			// The extension jar will take care of exporting
			javaOtelLogsExporterEnvVar: "none",
			javaOtelMetricsExporter:    "none",
			javaOtelTracesExporter:     "none",
		},
		Mounts: []*v1beta1.Mount{
			{
				ContainerPath: "/var/odigos/java-ext-ebpf",
				HostPath:      "/var/odigos/java-ext-ebpf",
				ReadOnly:      true,
			},
		},
	}
}
