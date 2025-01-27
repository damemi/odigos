package instrumentlang

import (
	"github.com/odigos-io/odigos/common"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)


func EnterpriseNodeJS(deviceId string, uniqueDestinationSignals map[common.ObservabilitySignal]struct{}) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{
		Mounts: []*v1beta1.Mount{
			{
				ContainerPath: "/var/odigos/nodejs-ebpf",
				HostPath:      "/var/odigos/nodejs-ebpf",
				ReadOnly:      true,
			},
		},
	}
}
