package instrumentlang

import "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

const (
	nodeOptionsEnvVar = "NODE_OPTIONS"
	nodeOptionsValue  = "--require /var/odigos/nodejs-ebpf/dist/loader.js"
)

func EnterpriseNodeJS(deviceId string) *v1beta1.ContainerAllocateResponse {
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
