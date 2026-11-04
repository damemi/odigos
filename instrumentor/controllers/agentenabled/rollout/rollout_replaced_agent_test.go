package rollout_test

import (
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1alpha1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/instrumentor/controllers/agentenabled/rollout"
	"github.com/odigos-io/odigos/instrumentor/internal/testutil"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// mockICNoRestartDistro is an InstrumentationConfig whose container moved to a distribution that
// needs no restart, after the workload was rolled out for an earlier configuration.
func mockICNoRestartDistro(base *odigosv1alpha1.InstrumentationConfig) *odigosv1alpha1.InstrumentationConfig {
	base.Spec.Containers = []odigosv1alpha1.ContainerAgentConfig{
		{
			ContainerName:                "test",
			AgentEnabled:                 true,
			OtelDistroName:               "opentelemetry-ebpf-instrumentation", // noRestartRequired
			PodManifestInjectionOptional: true,
		},
	}
	base.Spec.AgentInjectionEnabled = true
	base.Spec.PodManifestInjectionOptional = true
	base.Spec.AgentsMetaHash = "no-restart-hash"
	base.Status.WorkloadRolloutHash = "earlier-hash"
	return base
}

// newMockPodWithDistro is a running pod of the deployment that the webhook created for distroName.
func newMockPodWithDistro(ns *corev1.Namespace, deploymentName, podName, distroName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns.Name,
			Labels: map[string]string{
				"app.kubernetes.io/name":            deploymentName,
				k8sconsts.OdigosAgentsMetaHashLabel: "earlier-hash",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "test",
				Image: "test",
				Env:   []corev1.EnvVar{{Name: k8sconsts.OdigosEnvVarDistroName, Value: distroName}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// A workload switched to a distribution that needs no restart still runs the agent its pods were
// created with. A restart is the only way to remove it, so the workload is rolled out once.
func Test_Rollout_NoRestartDistroReplacesAgentThatNeededRestart(t *testing.T) {
	s := newTestSetup()
	deployment := testutil.NewMockTestDeployment(s.ns, "test-deployment")
	ic := mockICNoRestartDistro(testutil.NewMockInstrumentationConfig(deployment))
	pod := newMockPodWithDistro(s.ns, deployment.Name, "old-agent-pod", "python-community")
	pw := k8sconsts.PodWorkload{Name: deployment.Name, Namespace: deployment.Namespace, Kind: k8sconsts.WorkloadKindDeployment}
	fakeClient := s.newFakeClient(deployment, pod)

	rolloutResult, err := rollout.Do(s.ctx, fakeClient, ic, pw, s.conf, s.distroProvider, newRolloutConcurrencyLimiterNoLimit())

	assertTriggeredRolloutWithRequeue(t, rolloutResult, err)
	assertWorkloadRestarted(t, s.ctx, fakeClient, pw)
	assert.Equal(t, "no-restart-hash", ic.Status.WorkloadRolloutHash)
	assert.Equal(t, string(odigosv1alpha1.WorkloadRolloutReasonTriggeredSuccessfully), ic.Status.Conditions[0].Reason)
}

// Pods created for the current distribution, or for an earlier one that needed no restart either,
// carry no agent a restart would remove: the workload is left running.
func Test_NoRollout_PodsCarryNoAgentThatNeededRestart(t *testing.T) {
	for _, podDistro := range []string{"opentelemetry-ebpf-instrumentation", "", "unknown-distro"} {
		t.Run(podDistro, func(t *testing.T) {
			s := newTestSetup()
			deployment := testutil.NewMockTestDeployment(s.ns, "test-deployment")
			ic := mockICNoRestartDistro(testutil.NewMockInstrumentationConfig(deployment))
			pod := newMockPodWithDistro(s.ns, deployment.Name, "pod", podDistro)
			pw := k8sconsts.PodWorkload{Name: deployment.Name, Namespace: deployment.Namespace, Kind: k8sconsts.WorkloadKindDeployment}
			fakeClient := s.newFakeClient(deployment, pod)

			rolloutResult, err := rollout.Do(s.ctx, fakeClient, ic, pw, s.conf, s.distroProvider, newRolloutConcurrencyLimiterNoLimit())

			assertTriggeredRolloutNoRequeue(t, rolloutResult, err)
			assertWorkloadNotRestarted(t, s.ctx, fakeClient, pw)
			assert.Equal(t, string(odigosv1alpha1.WorkloadRolloutReasonNotRequired), ic.Status.Conditions[0].Reason)
		})
	}
}

// Once the workload was rolled out for the switch, pods still on their way out do not restart it
// again: the rollout is awaited, then reported finished.
func Test_NoSecondRollout_WhileReplacedPodsTerminate(t *testing.T) {
	s := newTestSetup()
	deployment := testutil.NewMockTestDeployment(s.ns, "test-deployment")
	ic := mockICNoRestartDistro(testutil.NewMockInstrumentationConfig(deployment))
	ic.Status.WorkloadRolloutHash = ic.Spec.AgentsMetaHash // the switch rollout already happened
	pod := newMockPodWithDistro(s.ns, deployment.Name, "old-agent-pod", "python-community")
	pw := k8sconsts.PodWorkload{Name: deployment.Name, Namespace: deployment.Namespace, Kind: k8sconsts.WorkloadKindDeployment}
	fakeClient := s.newFakeClient(deployment, pod)

	rolloutResult, err := rollout.Do(s.ctx, fakeClient, ic, pw, s.conf, s.distroProvider, newRolloutConcurrencyLimiterNoLimit())

	assert.NoError(t, err)
	assertWorkloadNotRestarted(t, s.ctx, fakeClient, pw)
	assert.Equal(t, "no-restart-hash", ic.Status.WorkloadRolloutHash)
	assert.True(t, rolloutResult.StatusChanged)
	assert.Equal(t, string(odigosv1alpha1.WorkloadRolloutReasonRolloutFinished), ic.Status.Conditions[0].Reason)
}
