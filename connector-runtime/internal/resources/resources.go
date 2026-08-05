// Package resources builds the Kubernetes objects (Deployment + Service) that run a provider connector
// for an OdigosCloudConnector, plus the naming/labeling helpers the controller uses.
package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const (
	// GraphQLPort is the port the connector runtime serves its GraphQL + health endpoints on.
	GraphQLPort = 8080
	// CLIPort is the port the connector serves the odictl CLI WebSocket (/ws) on. It matches odictl's
	// default --port so `kubectl port-forward 23678:23678` + `odictl` works with no flags.
	CLIPort         = 23678
	containerName   = "connector"
	graphQLPortName = "graphql"
	cliPortName     = "odictl"

	// DefaultShutdownGraceSeconds is the pod termination grace period for connector workloads. It must
	// EXCEED the default ODIGOS_SHUTDOWN_TIMEOUT (10m) with margin: the rollback may consume its whole
	// budget, and the drain-complete write that follows (MarkShutdownComplete — what lets the controller
	// remove the finalizer) must still land before SIGKILL. Equal values left no headroom, so a
	// long-but-successful rollback could be killed before recording completion, stalling the delete to the
	// controller's cleanup timeout and reporting a spurious CleanupFailed.
	DefaultShutdownGraceSeconds int64 = 660

	LabelComponent      = "odigos.io/component"
	LabelPlatformType   = "odigos.io/platform-type"
	LabelProvider       = "odigos.io/provider"
	LabelCloudConnector = "odigos.io/cloud-connector"
	// LabelWorkload is the unique, bounded pod-selector identity for a connector's Deployment/Service.
	// Its value is the hashed WorkloadName (≤63 chars, includes provider + a content hash), so the
	// selector is unique per (provider, connector) and never exceeds the label-value length limit — unlike
	// the raw connector name, which can. The Deployment and Service select ONLY on this label so a Service
	// can never fan out to pods from a different provider's connector or a stale leftover Deployment.
	LabelWorkload = "odigos.io/connector-workload"

	ComponentValue    = "connector-runtime"
	PlatformTypeValue = "connector"

	// MaxReplicas caps connector runtime replicas. The runtime is NOT leader-elected and a single
	// connector shares one heartbeat row and one reconcile loop, so more than one replica would race
	// provider mutations. Until locking/leader election exists, the controller clamps to this.
	MaxReplicas int32 = 1
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// WorkloadName returns the deterministic Deployment/Service name for a connector, sanitized and
// suffixed with a stable hash, bounded to the 63-char label limit.
func WorkloadName(provider, name string) string {
	base := fmt.Sprintf("odigos-connector-%s-%s", sanitize(provider), sanitize(name))
	sum := sha256.Sum256([]byte(provider + "/" + name))
	suffix := hex.EncodeToString(sum[:])[:8]
	full := base + "-" + suffix
	if len(full) > 63 {
		keep := 63 - 1 - len(suffix)
		full = strings.TrimRight(base[:keep], "-") + "-" + suffix
	}
	return full
}

func sanitize(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// Labels returns the labels applied to a connector's workload and pods. LabelCloudConnector carries the
// (length-bounded) connector name for humans; LabelWorkload is the unique selector identity.
func Labels(cr *cloudconnectorsv1.OdigosCloudConnector) map[string]string {
	return map[string]string{
		LabelComponent:      ComponentValue,
		LabelPlatformType:   PlatformTypeValue,
		LabelProvider:       cr.Spec.Provider,
		LabelCloudConnector: labelValue(cr.Name),
		LabelWorkload:       WorkloadName(cr.Spec.Provider, cr.Name),
	}
}

// selectorLabels returns the immutable pod-selector subset: only the unique, bounded workload label. The
// Deployment selector is immutable, so it must be a stable value — WorkloadName is deterministic for a
// connector (provider is immutable, enforced by the controller).
func selectorLabels(workloadName string) map[string]string {
	return map[string]string{LabelWorkload: workloadName}
}

// labelValue makes an arbitrary string a valid Kubernetes label value: lowercased, non-alphanumerics
// collapsed to '-', trimmed, and truncated to the 63-char limit. Used for the informational
// cloud-connector label, whose source (the connector name) can exceed the limit.
func labelValue(s string) string {
	v := sanitize(s)
	if len(v) > 63 {
		v = strings.Trim(v[:63], "-")
	}
	return v
}

// ReplicasFor returns the replica count to run for a connector, clamped to MaxReplicas, and whether the
// requested value was clamped (so the controller can surface a warning condition). A 0 (intentional
// disable) passes through; values above MaxReplicas are clamped down.
func ReplicasFor(cr *cloudconnectorsv1.OdigosCloudConnector) (replicas int32, clamped bool) {
	requested := int32(1)
	if cr.Spec.Runtime.Replicas != nil {
		requested = *cr.Spec.Runtime.Replicas
	}
	if requested > MaxReplicas {
		return MaxReplicas, true
	}
	return requested, false
}

// DeploymentParams carries controller-level configuration into the object builders.
type DeploymentParams struct {
	Namespace     string
	WorkloadName  string
	Image         string
	DBSecretName  string
	DBSecretKey   string
	SchemaName    string // e.g. connector_aws
	SchemaVersion string // expected schema version stamped into the runtime
	// CentralBackendURL is dialed by the connector for the reverse-WebSocket agents-api connection.
	CentralBackendURL string
}

// BuildDeployment constructs the connector Deployment (strategy: Recreate) for a CR.
func BuildDeployment(cr *cloudconnectorsv1.OdigosCloudConnector, p DeploymentParams) *appsv1.Deployment {
	labels := Labels(cr)
	credSecret := cr.Spec.CredentialsSecretRef.Name

	replicas, _ := ReplicasFor(cr)

	env := []corev1.EnvVar{
		{Name: "ODIGOS_CONNECTOR_NAME", Value: cr.Name},
		// Stable, cluster-unique id used as the Central proxy/routing id (distinct from the display name), so
		// two clusters that each name their connector the same do not collide on one routing entry.
		{Name: "ODIGOS_CONNECTOR_ID", Value: string(cr.UID)},
		{Name: "ODIGOS_PROVIDER", Value: cr.Spec.Provider},
		{Name: "ODIGOS_LOG_LEVEL", Value: "debug"},
		{Name: "ODIGOS_DB_SCHEMA", Value: p.SchemaName},
		{Name: "ODIGOS_EXPECTED_SCHEMA_VERSION", Value: p.SchemaVersion},
		{Name: "ODIGOS_CAPABILITIES", Value: capabilitiesJSON(cr)},
		// The provider boundary id (spec.account.id) is stamped into every provider's boundary env var.
		// A connector reads only the var for its own provider; the others are harmless no-ops. Add a new
		// line here when onboarding a provider whose boundary env var differs.
		{Name: "ODIGOS_AWS_ACCOUNT_ID", Value: cr.Spec.Account.ID},
		{Name: "ODIGOS_GCP_PROJECT_ID", Value: cr.Spec.Account.ID},
		{Name: "ODIGOS_AZURE_SUBSCRIPTION_ID", Value: cr.Spec.Account.ID},
		{Name: "ODIGOS_CENTRAL_BACKEND_URL", Value: p.CentralBackendURL},
		{Name: "ODIGOS_GQL_ADDR", Value: fmt.Sprintf(":%d", GraphQLPort)},
		{Name: "ODIGOS_CLI_ADDR", Value: fmt.Sprintf(":%d", CLIPort)},
		// Shutdown timeout is DB-backed operational config (config.Manager), not env.
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		dsnEnv("DATABASE_URL", p.DBSecretName, p.DBSecretKey),
	}

	// Discovery interval + tag selectors are NOT stamped as env: they are operational config, held in the
	// DB with defaults and tuned live via the agents-api config/updateConfig operations (no rollout). Only
	// bootstrap config (identity, provider, account, DSN, capabilities, credentials) is stamped here.

	// Credentials come from the credentials Secret, wired generically from the provider spec's auth
	// definition: each auth field maps its Secret key (field name) to the env var the connector reads.
	// All optional, so any supported auth method (static keys, assume-role, ambient workload identity)
	// works without the runtime knowing the provider's auth shape.
	if providerSpec, ok := spec.Get(cr.Spec.Provider); ok {
		for _, f := range providerSpec.CredentialFields() {
			if f.EnvVar == "" {
				continue
			}
			env = append(env, secretEnv(f.EnvVar, credSecret, f.Name))
		}
	}

	container := corev1.Container{
		Name:            containerName,
		Image:           p.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             env,
		Ports: []corev1.ContainerPort{
			{Name: graphQLPortName, ContainerPort: GraphQLPort},
			{Name: cliPortName, ContainerPort: CLIPort},
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intStrPort()}},
			InitialDelaySeconds: 3,
			PeriodSeconds:       10,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intStrPort()}},
			InitialDelaySeconds: 10,
			PeriodSeconds:       20,
		},
		// Baseline container hardening for a credential-bearing pod. Deliberately not setting runAsNonRoot
		// or readOnlyRootFilesystem here to avoid assumptions about the connector image's user/layout;
		// these two are safe regardless.
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
	if cr.Spec.Runtime.Resources != nil {
		container.Resources = *cr.Spec.Runtime.Resources
	}

	return &appsv1.Deployment{
		// TypeMeta is required for server-side apply.
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.WorkloadName,
			Namespace: p.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			// One runtime owns one account boundary; Recreate avoids two steady-state pods.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels(p.WorkloadName)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To(DefaultShutdownGraceSeconds),
					// The connector talks to Postgres, the cloud provider, and Central — never the Kubernetes
					// API — so don't mount a service-account token into a pod that holds cloud credentials.
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{container},
				},
			},
		},
	}
}

// BuildService constructs the ClusterIP Service exposing the connector's GraphQL endpoint.
func BuildService(cr *cloudconnectorsv1.OdigosCloudConnector, p DeploymentParams) *corev1.Service {
	return &corev1.Service{
		// TypeMeta is required for server-side apply.
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.WorkloadName,
			Namespace: p.Namespace,
			Labels:    Labels(cr),
		},
		Spec: corev1.ServiceSpec{
			Selector: selectorLabels(p.WorkloadName),
			Ports: []corev1.ServicePort{{
				Name:       graphQLPortName,
				Port:       GraphQLPort,
				TargetPort: intStrPort(),
			}},
		},
	}
}

func intStrPort() intstr.IntOrString { return intstr.FromInt32(GraphQLPort) }

// capabilitiesJSON encodes the CR's per-type capabilities (the customer's per-domain allow-list) for the
// connector to consume via ODIGOS_CAPABILITIES. The shape matches spec.Capabilities: an object with
// `workloads` and `computePlatforms` lists.
func capabilitiesJSON(cr *cloudconnectorsv1.OdigosCloudConnector) string {
	b, err := json.Marshal(struct {
		Workloads        []cloudconnectorsv1.CloudConnectorWorkloadCapability        `json:"workloads,omitempty"`
		ComputePlatforms []cloudconnectorsv1.CloudConnectorComputePlatformCapability `json:"computePlatforms,omitempty"`
	}{
		Workloads:        cr.Spec.Workloads,
		ComputePlatforms: cr.Spec.ComputePlatforms,
	})
	if err != nil {
		return "{}"
	}
	return string(b)
}

func dsnEnv(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		},
	}}
}

func secretEnv(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
			Optional:             ptr.To(true),
		},
	}}
}
