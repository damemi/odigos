package instrumentlang

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/envOverwrite"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	pythonPathEnvVar         = "PYTHONPATH"
	pythonConfiguratorEnvVar = "OTEL_PYTHON_CONFIGURATOR"
	pythonConfiguratorValue  = "ebpf"
)

func EnterprisePython(deviceId string, uniqueDestinationSignals map[common.ObservabilitySignal]struct{}) *v1beta1.ContainerAllocateResponse {
	pythonPathValue, _ := envOverwrite.ValToAppend(pythonPathEnvVar, common.OtelSdkEbpfEnterprise)
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			pythonPathEnvVar:         pythonPathValue,
			pythonConfiguratorEnvVar: pythonConfiguratorValue,
		},
		Mounts: []*v1beta1.Mount{
			{
				ContainerPath: "/var/odigos/python-ebpf",
				HostPath:      "/var/odigos/python-ebpf",
				ReadOnly:      true,
			},
			{
				ContainerPath: "/var/odigos/python",
				HostPath:      "/var/odigos/python",
				ReadOnly:      true,
			},
		},
	}
}
