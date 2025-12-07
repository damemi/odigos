package wasp

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/wasp-handshake/sender"
)

const (
	waspInitContainerName         = "wasp-init"
	waspInitContainerImageName    = waspInitContainerName
	waspInitContainerImageVersion = "v0.0.6"
)

// PodMutator adds the wasp init container to the pod if it is not already present.
// if wasp is not enabled in the configuration, it returns an error.
func PodMutator(pod *corev1.Pod, config common.OdigosConfiguration) error {
	if config.WaspEnabled == nil {
		return errors.New("wasp config is not specified, hence not adding the wasp init container")
	}

	if !*config.WaspEnabled {
		return errors.New("wasp is not enabled in the configuration, hence not adding the wasp init container")
	}

	// make sure the wasp init container is not present, so this is idempotent
	// if it is present, this is a no-op.
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == waspInitContainerName {
			return nil
		}
	}

	odigosNamespace := env.GetCurrentNamespace()

	initContainer := corev1.Container{
		Name:  waspInitContainerName,
		Image: fmt.Sprintf("%s/%s:%s", config.ImagePrefix, waspInitContainerImageName, waspInitContainerImageVersion),
		Env: []corev1.EnvVar{
			{
				Name:  sender.TargetAddressEnvName,
				Value: fmt.Sprintf("http://%s.%s:%d", k8sconsts.OdigletLocalTrafficServiceName, odigosNamespace, k8sconsts.OdigletWaspServicePort),
			},
			{
				Name: sender.PodNameEnvName,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				},
			},
			{
				Name: sender.PodUIDEnvName,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.uid",
					},
				},
			},
		},
		SecurityContext: &corev1.SecurityContext{
			Capabilities: &corev1.Capabilities{
				Add: []corev1.Capability{"BPF"},
			},
		},
	}

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, initContainer)
	return nil
}