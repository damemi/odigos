package instrumentlang

import "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

const (
	goEnvGoDebug = "GODEBUG"
)

func EnterpriseGo(deviceId string) *v1beta1.ContainerAllocateResponse {
	return &v1beta1.ContainerAllocateResponse{
		Envs: map[string]string{
			// this is required for the goroutine context propagation feature
			// to work in the enterprise go instrumentation.
			goEnvGoDebug: "tracebackancestors=5",
		},
	}
}
