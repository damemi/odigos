package instrumentlang

import "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

func EnterpriseMySQL(deviceId string) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{}
}