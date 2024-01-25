package instrumentlang

import "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

const (
	javaToolOptionsEnvVar        = "JAVA_TOOL_OPTIONS"
	javaOptsEnvVar               = "JAVA_OPTS"
	javaToolOptionsPattern       = "-javaagent:/var/odigos/java-ebpf/dtrace-injector.jar"
)

func EnterpriseJava(deviceId string) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			javaToolOptionsEnvVar:        javaToolOptionsPattern,
			javaOptsEnvVar:               javaToolOptionsPattern,
		},
		Mounts: []*v1beta1.Mount{
			{
				ContainerPath: "/var/odigos/java-ebpf",
				HostPath:      "/var/odigos/java-ebpf",
				ReadOnly:      true,
			},
		},
	}
}
