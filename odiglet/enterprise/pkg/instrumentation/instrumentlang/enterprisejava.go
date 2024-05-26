package instrumentlang

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/envOverwrite"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	javaToolOptionsEnvVar        = "JAVA_TOOL_OPTIONS"
	javaOptsEnvVar               = "JAVA_OPTS"
)

func EnterpriseJava(deviceId string) *v1beta1.ContainerAllocateResponse {
	javaToolOptionsPattern, _ := envOverwrite.ValToAppend(javaToolOptionsEnvVar, common.OtelSdkEbpfEnterprise)
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
