package instrumentlang

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/envOverwrite"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	nodeOptionsEnvVar = "NODE_OPTIONS"
)

func EnterpriseNodeJS(deviceId string) *v1beta1.ContainerAllocateResponse {
	nodeOptionsValue, _ := envOverwrite.ValToAppend(nodeOptionsEnvVar, common.OtelSdkEbpfEnterprise)
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			nodeOptionsEnvVar: nodeOptionsValue,
		},
		Mounts: []*v1beta1.Mount{
			{
				ContainerPath: "/var/odigos/nodejs-ebpf",
				HostPath:      "/var/odigos/nodejs-ebpf",
				ReadOnly:      true,
			},
		},
	}
}
