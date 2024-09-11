package instrumentlang

import (
	"fmt"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/envOverwrite"
	"github.com/odigos-io/odigos/odiglet/pkg/env"
	"github.com/odigos-io/odigos/odiglet/pkg/instrumentation/consts"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	pythonPathEnvVar         = "PYTHONPATH"
	pythonConfiguratorEnvVar = "OTEL_PYTHON_CONFIGURATOR"
	pythonConfiguratorValue  = "ebpf"
	pythonOdigosOpampServer  = "ODIGOS_OPAMP_SERVER_HOST"
	pythonOdigosDeviceId     = "ODIGOS_INSTRUMENTATION_DEVICE_ID"
)

func EnterprisePython(deviceId string, uniqueDestinationSignals map[common.ObservabilitySignal]struct{}) *v1beta1.ContainerAllocateResponse {
	opampServerHost := fmt.Sprintf("%s:%d", env.Current.NodeIP, consts.OpAMPPort)
	pythonPathValue, _ := envOverwrite.ValToAppend(pythonPathEnvVar, common.OtelSdkEbpfEnterprise)

	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			pythonPathEnvVar:         pythonPathValue,
			pythonConfiguratorEnvVar: pythonConfiguratorValue,
			pythonOdigosDeviceId:     deviceId,
			pythonOdigosOpampServer:  opampServerHost,
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
