package resources

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/cli/cmd/resources/resourcemanager"
	"github.com/odigos-io/odigos/cli/pkg/kube"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type OBIResourceManager struct {
	client *kube.Client
	ns     string
	opts   resourcemanager.ManagerOpts
}

func NewOBIResourceManager(client *kube.Client, ns string, opts resourcemanager.ManagerOpts) resourcemanager.ResourceManager {
	return &OBIResourceManager{
		client: client,
		ns:     ns,
		opts:   opts,
	}
}

func (m *OBIResourceManager) Name() string {
	return "OBI (Odigos eBPF Instrumentation)"
}

func (m *OBIResourceManager) InstallFromScratch(ctx context.Context) error {
	resources := []kube.Object{
		NewOBIServiceAccount(m.ns),
		NewOBIClusterRole(),
		NewOBIClusterRoleBinding(m.ns),
		NewOBIConfigMap(m.ns),
		NewOBIDaemonSet(m.ns),
	}

	return m.client.ApplyResources(ctx, 1, resources, m.opts)
}

func NewOBIServiceAccount(ns string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8sconsts.OBIServiceAccountName,
			Namespace: ns,
			Labels: map[string]string{
				"app": k8sconsts.OBIDaemonSetName,
			},
		},
	}
}

func NewOBIClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRole",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: k8sconsts.OBIRoleName,
			Labels: map[string]string{
				"app": k8sconsts.OBIDaemonSetName,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets"},
				Verbs:     []string{"list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "nodes"},
				Verbs:     []string{"list", "watch"},
			},
		},
	}
}

func NewOBIClusterRoleBinding(ns string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRoleBinding",
			APIVersion: "rbac.authorization.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: k8sconsts.OBIRoleBindingName,
			Labels: map[string]string{
				"app": k8sconsts.OBIDaemonSetName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     k8sconsts.OBIRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      k8sconsts.OBIServiceAccountName,
				Namespace: ns,
			},
		},
	}
}

func NewOBIConfigMap(ns string) *corev1.ConfigMap {
	defaultConfig := `
discovery:
  instrument:
    - open_ports: 443
otel_metrics_export:
  features: ['application']
  endpoint: ` + fmt.Sprintf("http://%s:4317", k8sconsts.OdigosClusterCollectorDeploymentName)

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8sconsts.OBIConfigMapName,
			Namespace: ns,
			Labels: map[string]string{
				"app": k8sconsts.OBIDaemonSetName,
			},
		},
		Data: map[string]string{
			"obi-config.yml": defaultConfig,
		},
	}
}

func NewOBIDaemonSet(ns string) *appsv1.DaemonSet {
	// Define resource requirements
	memoryRequest := resource.MustParse("256Mi")
	cpuRequest := resource.MustParse("100m")
	memoryLimit := resource.MustParse("512Mi")
	cpuLimit := resource.MustParse("500m")

	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      k8sconsts.OBIDaemonSetName,
			Namespace: ns,
			Labels: map[string]string{
				"app": k8sconsts.OBIDaemonSetName,
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": k8sconsts.OBIDaemonSetName,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": k8sconsts.OBIDaemonSetName,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: k8sconsts.OBIServiceAccountName,
					HostPID:            true, // Required for DaemonSet mode to access all processes
					Containers: []corev1.Container{
						{
							Name:  "obi",
							Image: "otel/ebpf-instrument:main",
							Env: []corev1.EnvVar{
								{
									Name:  "OTEL_EBPF_CONFIG_PATH",
									Value: "/config/obi-config.yml",
								},
								{
									Name:  "OTEL_EBPF_TRACE_PRINTER",
									Value: "text",
								},
								{
									Name:  "OTEL_EBPF_KUBE_METADATA_ENABLE",
									Value: "autodetect",
								},
								{
									Name: "KUBE_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: boolPtr(true),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "obi-config",
									MountPath: "/config",
								},
								{
									Name:      "var-run-obi",
									MountPath: "/var/run/obi",
								},
								{
									Name:      "cgroup",
									MountPath: "/sys/fs/cgroup",
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceMemory: memoryRequest,
									corev1.ResourceCPU:    cpuRequest,
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: memoryLimit,
									corev1.ResourceCPU:    cpuLimit,
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       10,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/readyz",
										Port: intstr.FromInt(8080),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "obi-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: k8sconsts.OBIConfigMapName,
									},
								},
							},
						},
						{
							Name: "var-run-obi",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "cgroup",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/sys/fs/cgroup",
								},
							},
						},
					},
					Tolerations: []corev1.Toleration{
						{
							Effect:   corev1.TaintEffectNoSchedule,
							Operator: corev1.TolerationOpExists,
						},
						{
							Effect:   corev1.TaintEffectNoExecute,
							Operator: corev1.TolerationOpExists,
						},
					},
				},
			},
		},
	}
}

func boolPtr(b bool) *bool {
	return &b
}
