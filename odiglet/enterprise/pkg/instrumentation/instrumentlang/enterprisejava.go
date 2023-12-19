package instrumentlang

import "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

// for java we currently don't need any special environment variables or mounts
func EnterpriseJava(deviceId string) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{}
}
