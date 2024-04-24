package instrumentlang

import "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

const (
	javaOtelLogsExporterEnvVar   = "OTEL_LOGS_EXPORTER"
	javaOtelTracesExporter       = "OTEL_TRACES_EXPORTER"
	javaOtelMetricsExporter      = "OTEL_METRICS_EXPORTER"
	javaToolOptionsPatternNativeEnterprise = "-javaagent:/var/odigos/java-ext-ebpf/javaagent.jar " +
	"-Dotel.javaagent.extensions=/var/odigos/java-ext-ebpf/otel_agent_extension.jar"
)

func NativeEnterpriseJava(deviceId string) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			javaToolOptionsEnvVar:        javaToolOptionsPatternNativeEnterprise,
			javaOptsEnvVar:               javaToolOptionsPatternNativeEnterprise,
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
