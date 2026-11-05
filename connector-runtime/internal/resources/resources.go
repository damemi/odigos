// Package resources builds the Kubernetes objects (Deployment + Service) that run a provider connector
// for an OdigosCloudConnector, plus the naming/labeling helpers the controller uses.
package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
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
	// OpAMPPort is the connector's OpAMP listener, reached only by connector-opamp-gateway pods. Named
	// and numbered from connectors/opamp so the runtime that opens the port, the gateway that dials it
	// and the base that serves it cannot disagree.
	opampPortName = opamp.ServicePortName
	OpAMPPort     = opamp.ServerPort

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
	// LabelConnectorID is the CR UID, and it is how connector-opamp-gateway finds this connector's
	// Service — by label, never by recomputing the workload name below. Applied only to a connector that
	// manages a Collector: it is part of the pod template, so adding it unconditionally would roll every
	// connector pod in an installation that does not use the feature.
	LabelConnectorID = opamp.LabelConnectorID
	// LabelWorkload is the unique, bounded pod-selector identity for a connector's Deployment/Service.
	// Its value is the hashed WorkloadName (≤63 chars, includes provider + a content hash), so the
	// selector is unique per (provider, connector) and never exceeds the label-value length limit — unlike
	// the raw connector name, which can. The Deployment and Service select ONLY on this label so a Service
	// can never fan out to pods from a different provider's connector or a stale leftover Deployment.
	LabelWorkload = "odigos.io/connector-workload"

	ComponentValue    = "connector-runtime"
	PlatformTypeValue = "connector"

	// The enterprise licence secret this install already has: the same Secret and the same key every
	// other enterprise component reads its ODIGOS_ONPREM_TOKEN from. Named here rather than inlined so
	// a rename of either has one place to change, and so the connector reads the install's own token
	// rather than anything issued for it separately.
	centralSecretName           = "odigos-central"
	centralSecretOnPremTokenKey = "ODIGOS_ONPREM_TOKEN"
	// envOnPremToken must equal collectorimage.EnvOnPremToken, which the connector reads and which the
	// enterprise Collector's auth extension requires. Spelled here rather than imported: it lives in
	// connectors/base, a module this one does not depend on and should not start depending on for one
	// string — base pulls the database driver and the whole domain layer. It is a stable, externally
	// documented name (the same variable every enterprise Odigos component takes), and
	// TestConnectorGetsTheOnPremToken pins the spelling.
	envOnPremToken = "ODIGOS_ONPREM_TOKEN"

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

// ManagesCollectors reports whether this connector has a managed-Collector capability it could honor.
//
// The gate for every piece of OpAMP plumbing below: the port, the label and the NetworkPolicy exist
// exactly when something serves them, which is the same condition base.Run starts the OpAMP server on.
// A connector with no Collector is generated byte-identically to before this feature existed — no extra
// port to explain, no pod roll, and no NetworkPolicy on a pod that never had one.
//
// Uses the resolved capability, so it is true for the spec field and for the compatibility annotation
// alike, and false for a list that was refused.
func ManagesCollectors(cr *cloudconnectorsv1.OdigosCloudConnector) bool {
	collectors, _ := CollectorCapabilities(cr)
	return len(collectors) > 0
}

// Labels returns the labels applied to a connector's workload and pods. LabelCloudConnector carries the
// (length-bounded) connector name for humans; LabelWorkload is the unique selector identity.
func Labels(cr *cloudconnectorsv1.OdigosCloudConnector) map[string]string {
	labels := map[string]string{
		LabelComponent:      ComponentValue,
		LabelPlatformType:   PlatformTypeValue,
		LabelProvider:       cr.Spec.Provider,
		LabelCloudConnector: labelValue(cr.Name),
		LabelWorkload:       WorkloadName(cr.Spec.Provider, cr.Name),
	}
	if ManagesCollectors(cr) {
		// The gateway's only way to find this connector's Service. Conditional because this map is the
		// pod template's labels too, so an unconditional entry would roll every connector pod once.
		labels[LabelConnectorID] = string(cr.UID)
	}
	return labels
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
	// FargateAgentsImage optionally overrides the odigos-fargate-agents bundle for AWS connectors
	// (stamped as ODIGOS_FARGATE_AGENTS_IMAGE); empty keeps the connector's compiled-in default.
	FargateAgentsImage string
	// AgentCoreAgentsImage overrides the carrier image that manual-mode AgentCore instructions tell customers to
	// copy from. The connector never pulls it.
	AgentCoreAgentsImage  string
	ManagedCollectorImage string
	// OpAMPEndpoint is the public endpoint a managed Collector connects back to, per environment.
	// Already resolved by the caller (see OpAMPEndpoint) — empty means unconfigured or unusable, and
	// the connector deploys no Collector rather than one that can never be configured.
	OpAMPEndpoint string
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
		{Name: "ODIGOS_BOOTSTRAP_REGION", Value: cr.Spec.BootstrapRegion},
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
		{Name: "ODIGOS_CLI_ADDR", Value: fmt.Sprintf("127.0.0.1:%d", CLIPort)},
		// Shutdown timeout is DB-backed operational config (config.Manager), not env.
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		dsnEnv("DATABASE_URL", p.DBSecretName, p.DBSecretKey),
	}

	// The public OpAMP endpoint, stamped only when it resolved. Omitted rather than stamped empty so a
	// connector that has one and a connector that does not differ by the presence of the variable, not
	// by its value — and so turning the feature off does not leave a variable claiming otherwise.
	if p.OpAMPEndpoint != "" {
		env = append(env, corev1.EnvVar{Name: opamp.EnvEndpoint, Value: p.OpAMPEndpoint})
	}
	if ManagesCollectors(cr) && p.ManagedCollectorImage != "" {
		env = append(env, corev1.EnvVar{Name: "ODIGOS_COLLECTOR_IMAGE", Value: p.ManagedCollectorImage})
	}

	// The install's enterprise licence token, from the same odigos-central secret every other enterprise
	// component reads. A connector needs it for ONE thing: a managed Collector refuses to start without
	// a valid ODIGOS_ONPREM_TOKEN, and has the connector copy this install's
	// own value into the customer's secret store — so no Odigos-vendor licence material enters a
	// customer account and no token crosses between installs.
	//
	// Optional, like every other secret env here: a community install has no odigos-central token, and a
	// connector that cannot manage a Collector must still start and run its other two domains. The
	// connector reports the absence per Collector instead.
	env = append(env, secretEnv(envOnPremToken, centralSecretName, centralSecretOnPremTokenKey))

	// Optional operator-level override of the Fargate agents bundle (AWS-only concern; the connector
	// falls back to its compiled-in default when unset, so this env is stamped only when configured).
	if cr.Spec.Provider == "aws" && p.FargateAgentsImage != "" {
		env = append(env, corev1.EnvVar{Name: "ODIGOS_FARGATE_AGENTS_IMAGE", Value: p.FargateAgentsImage})
	}
	if cr.Spec.Provider == "aws" && p.AgentCoreAgentsImage != "" {
		env = append(env, corev1.EnvVar{Name: "ODIGOS_AGENTCORE_AGENTS_IMAGE", Value: p.AgentCoreAgentsImage})
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
		Ports:           containerPorts(cr),
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

// containerPorts declares the connector's listeners. OpAMP only for a connector that manages a
// Collector, because base.Run starts that server on the same condition — a declared port nothing listens
// on is a port to explain.
func containerPorts(cr *cloudconnectorsv1.OdigosCloudConnector) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{
		{Name: graphQLPortName, ContainerPort: GraphQLPort},
		{Name: cliPortName, ContainerPort: CLIPort},
	}
	if ManagesCollectors(cr) {
		ports = append(ports, corev1.ContainerPort{Name: opampPortName, ContainerPort: OpAMPPort})
	}
	return ports
}

// servicePorts exposes what other pods reach.
//
// The odictl port is deliberately absent from the Service, as it always has been: it is reached by
// port-forward, not by anything in the cluster. OpAMP is the opposite — the gateway is a different pod
// and the Service is how it finds this one.
func servicePorts(cr *cloudconnectorsv1.OdigosCloudConnector) []corev1.ServicePort {
	ports := []corev1.ServicePort{{
		Name:       graphQLPortName,
		Port:       GraphQLPort,
		TargetPort: intStrPort(),
	}}
	if ManagesCollectors(cr) {
		ports = append(ports, corev1.ServicePort{
			Name:       opampPortName,
			Port:       OpAMPPort,
			TargetPort: intstr.FromInt32(OpAMPPort),
		})
	}
	return ports
}

// BuildNetworkPolicy restricts the connector's OpAMP port to connector-opamp-gateway pods.
//
// Returns nil for a connector that manages no Collector — there is no port to restrict, and a pod that
// had no NetworkPolicy should not gain one for a feature it does not use.
//
// The non-obvious part is everything the policy has to RE-PERMIT. A pod with no NetworkPolicy accepts
// everything; the moment one selects it, every port not explicitly allowed is denied. A policy that
// mentioned only 4320 would therefore take out the connector's GraphQL endpoint and
// — on CNIs that police node-to-pod traffic — its own readiness and liveness probes. So the other ports
// are allowed from anywhere only for query-only HTTP GraphQL. The admin CLI
// binds to loopback and is reached by an authorized port-forward.
//
// gatewayNamespace scopes where gateway pods are looked for. Empty means this connector's own namespace,
// which is where the odigos-central chart puts both.
func BuildNetworkPolicy(cr *cloudconnectorsv1.OdigosCloudConnector, p DeploymentParams, gatewayNamespace string) *networkingv1.NetworkPolicy {
	if !ManagesCollectors(cr) {
		return nil
	}

	// The gateway selector. A namespace selector is added only when the gateway runs somewhere else,
	// because an empty NamespaceSelector means EVERY namespace — the opposite of the default anybody
	// would assume, and it would admit a pod with these labels from anywhere in the cluster.
	gatewayPeer := networkingv1.NetworkPolicyPeer{
		PodSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{opamp.LabelComponent: opamp.GatewayComponent},
		},
	}
	if gatewayNamespace != "" && gatewayNamespace != p.Namespace {
		gatewayPeer.NamespaceSelector = &metav1.LabelSelector{
			MatchLabels: map[string]string{corev1.LabelMetadataName: gatewayNamespace},
		}
	}

	opampPort := intstr.FromInt32(OpAMPPort)
	graphQL := intstr.FromInt32(GraphQLPort)
	tcp := corev1.ProtocolTCP

	return &networkingv1.NetworkPolicy{
		// TypeMeta is required for server-side apply.
		TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.WorkloadName,
			Namespace: p.Namespace,
			Labels:    Labels(cr),
		},
		Spec: networkingv1.NetworkPolicySpec{
			// The same selector the Deployment and Service use: unique per (provider, connector) and
			// length-bounded, so this policy can never select another connector's pods.
			PodSelector: metav1.LabelSelector{MatchLabels: selectorLabels(p.WorkloadName)},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					// The point of the policy: OpAMP from the gateway and nothing else. The port carries
					// no authentication of its own — the gateway authenticated the credential and the
					// identity it asserts is what the connector acts on — so this rule is what stands
					// between that identity and anything that could forge it.
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &opampPort}},
					From:  []networkingv1.NetworkPolicyPeer{gatewayPeer},
				},
				{
					// HTTP GraphQL is query-only. The admin CLI binds to pod loopback
					// and is reached through an authorized kubectl port-forward.
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &graphQL},
					},
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
			Ports:    servicePorts(cr),
		},
	}
}

func intStrPort() intstr.IntOrString { return intstr.FromInt32(GraphQLPort) }

// OpAMPEndpoint resolves the public OpAMP endpoint to stamp onto this connector, and reports why it
// could not when a managed Collector was actually asked for.
//
// The endpoint is per environment and comes from the odigos-central chart, so it is the same for every
// connector in a cluster. The reporting is not: a connector with no managed-Collector capability has no
// use for it, and warning about an unset endpoint on every connector in an installation that does not
// use the feature is how an operator learns to ignore the warning that matters.
//
// Returning empty is always the safe direction. Without the endpoint the connector has nowhere to point
// a Collector, so it deploys none — rather than deploying one that reaches nothing, holds a credential,
// and reports as running.
func OpAMPEndpoint(cr *cloudconnectorsv1.OdigosCloudConnector, configured string) (string, []string) {
	endpoint, err := opamp.Resolve(configured)
	if err == nil {
		return endpoint, nil
	}

	collectors, _ := CollectorCapabilities(cr)
	if len(collectors) == 0 {
		return "", nil
	}

	if errors.Is(err, opamp.ErrNotConfigured) {
		return "", []string{fmt.Sprintf(
			"this connector is allowed to manage a Collector, but no public OpAMP endpoint is configured, "+
				"so no Collector will be deployed. Set cloudConnectors.managedCollectors.externalEndpoint in "+
				"the odigos-central chart to the address the Collectors reach Central on, normally "+
				"wss://<central-host>%s", opamp.DefaultPath)}
	}
	return "", []string{fmt.Sprintf(
		"the configured public OpAMP endpoint cannot be used, so no Collector will be deployed: %v", err)}
}

// AnnotationCollectorsCapability carries the managed-Collector allow-list on clusters whose
// OdigosCloudConnector CRD predates spec.collectors[].
//
// Temporary, and deliberately annotation-shaped: an annotation needs no CRD change, so a cluster can
// enable a managed Collector before the CRD shipped by the odigos-central chart is upgraded. The value
// is the same JSON as the field:
//
//	odigos.io/collectors-capability: '[{"type":"aws.fargate-collector","management":true}]'
//
// spec.collectors[] wins wherever it is set. Remove this once every cluster is on a CRD that has the
// field — a shim that nothing needs is a second place for the allow-list to disagree with itself.
const AnnotationCollectorsCapability = "odigos.io/collectors-capability"

// CollectorCapabilities resolves the managed-Collector allow-list to hand the connector, and reports
// anything it refused to pass on.
//
// Refusing is always the safe direction. Without the capability the connector runs with no Collector
// deployer at all — spec.ActiveCollectorDeploymentTypes returns nothing — so nothing is created in the
// customer's cloud account. Passing a contradictory list through instead would make the connector
// refuse to start (config.FromEnv validates it), which takes workload instrumentation down with it
// over a Collector the customer may not even have deployed yet.
func CollectorCapabilities(cr *cloudconnectorsv1.OdigosCloudConnector) ([]spec.CollectorCapability, []string) {
	entries, problems := collectorCapabilitySource(cr)
	if len(entries) == 0 {
		return nil, problems
	}

	// Validated through spec.Capabilities so the controller and the connector cannot disagree about what
	// a legal allow-list is — the connector runs the same check on the JSON this produces.
	if err := (spec.Capabilities{Collectors: entries}).Validate(); err != nil {
		// The whole list goes, not the offending entry. A duplicate with conflicting flags or a
		// deployment-without-management entry means the list was authored wrong, and honoring the rest of
		// it would be guessing at intent — on the one capability that spends the customer's money.
		return nil, append(problems, fmt.Sprintf(
			"dropping the managed-Collector capability: %v. The connector was deployed without it, so no "+
				"Collector infrastructure will be created", err))
	}
	return entries, problems
}

// collectorCapabilitySource picks between the field and the compatibility annotation.
func collectorCapabilitySource(cr *cloudconnectorsv1.OdigosCloudConnector) ([]spec.CollectorCapability, []string) {
	raw, annotated := cr.Annotations[AnnotationCollectorsCapability]
	annotated = annotated && strings.TrimSpace(raw) != ""

	if len(cr.Spec.Collectors) > 0 {
		var problems []string
		if annotated {
			// Said out loud rather than ignored quietly: a shadowed annotation is exactly what somebody
			// edits when they want the capability to change, and then wonders why nothing happened.
			problems = append(problems, fmt.Sprintf(
				"spec.collectors is set, so the %s annotation is ignored; remove the annotation",
				AnnotationCollectorsCapability))
		}
		return collectorCapabilitiesFromCR(cr.Spec.Collectors), problems
	}

	if !annotated {
		return nil, nil
	}
	entries, err := parseCollectorsAnnotation(raw)
	if err != nil {
		return nil, []string{fmt.Sprintf(
			"ignoring the %s annotation: %v. The connector was deployed without the managed-Collector "+
				"capability, so no Collector infrastructure will be created", AnnotationCollectorsCapability, err)}
	}
	return entries, nil
}

// collectorCapabilitiesFromCR converts the CR's entries to the type the connector parses.
//
// The two are the same JSON, which is why only this domain is converted: it is the one with a second
// source and a validation step, and both need one canonical in-memory form to work on. Converting
// rather than re-marshalling the CR types also means the bytes handed to the connector are produced
// from the exact type it unmarshals into, so the shapes cannot drift apart.
func collectorCapabilitiesFromCR(in []cloudconnectorsv1.CloudConnectorCollectorCapability) []spec.CollectorCapability {
	out := make([]spec.CollectorCapability, 0, len(in))
	for _, c := range in {
		out = append(out, spec.CollectorCapability{
			Type:       c.Type,
			Management: c.Management,
			Deployment: c.Deployment,
		})
	}
	return out
}

// parseCollectorsAnnotation decodes the compatibility annotation.
func parseCollectorsAnnotation(raw string) ([]spec.CollectorCapability, error) {
	var entries []spec.CollectorCapability
	dec := json.NewDecoder(strings.NewReader(raw))
	// Strict, unlike the connector's own parse of ODIGOS_CAPABILITIES. The connector must tolerate keys
	// from a newer controller; an annotation is hand-written, so an unknown key there is a typo, and
	// silently ignoring it would enable something other than what was asked for.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("not a JSON array of {type, management, deployment}: %w", err)
	}

	for i, e := range entries {
		// The CRD marks type required, so the API server enforces this on the field. Nothing enforces it
		// on an annotation, and an entry with no type matches no provider type — it would sit in the
		// allow-list looking enabled and do nothing.
		if strings.TrimSpace(e.Type) == "" {
			return nil, fmt.Errorf("entry %d has no type", i)
		}
	}
	return entries, nil
}

// capabilitiesJSON encodes the CR's per-type capabilities (the customer's per-domain allow-list) for the
// connector to consume via ODIGOS_CAPABILITIES. The shape matches spec.Capabilities: an object with
// one key per domain.
//
// `collectors` is omitted entirely when there is none, so this produces byte-identical output to
// before the domain existed for every connector that does not use one. That matters because the value
// is a container env var: a key that appeared unconditionally would roll every connector pod in the
// fleet on upgrade.
//
// A connector image older than the collectors domain unmarshals this with encoding/json and no strict
// mode, so it ignores the key and wires no Collector deployer — which is how an old image stays safe
// in front of a new controller rather than needing to be version-gated here.
func capabilitiesJSON(cr *cloudconnectorsv1.OdigosCloudConnector) string {
	collectors, _ := CollectorCapabilities(cr)

	b, err := json.Marshal(struct {
		Workloads        []cloudconnectorsv1.CloudConnectorWorkloadCapability        `json:"workloads,omitempty"`
		ComputePlatforms []cloudconnectorsv1.CloudConnectorComputePlatformCapability `json:"computePlatforms,omitempty"`
		Collectors       []spec.CollectorCapability                                  `json:"collectors,omitempty"`
	}{
		Workloads:        cr.Spec.Workloads,
		ComputePlatforms: cr.Spec.ComputePlatforms,
		Collectors:       collectors,
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
