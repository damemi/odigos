package resources

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
)

const testUID = "3f2b1c9e-0000-4a00-8000-000000000001"

// withUID gives the CR the UID the gateway routes by.
func withUID(cr *cloudconnectorsv1.OdigosCloudConnector) { cr.UID = types.UID(testUID) }

// capabilityFromField and capabilityFromAnnotation are the two ways a managed-Collector capability
// arrives. Every piece of OpAMP plumbing must appear identically for both — a cluster whose CRD predates
// spec.collectors[] is exactly the one that needs the plumbing in order to be testable at all.
func capabilityFromField(cr *cloudconnectorsv1.OdigosCloudConnector) {
	withUID(cr)
	withCollectors(cloudconnectorsv1.CloudConnectorCollectorCapability{
		Type: collectorType, Management: true, Deployment: true,
	})(cr)
}

func capabilityFromAnnotation(cr *cloudconnectorsv1.OdigosCloudConnector) {
	withUID(cr)
	annotate(`[{"type":"aws.fargate-collector","management":true,"deployment":true}]`)(cr)
}

func noCapability(cr *cloudconnectorsv1.OdigosCloudConnector) { withUID(cr) }

// refusedCapability is a list the connector would refuse to start on, so nothing is passed to it.
func refusedCapability(cr *cloudconnectorsv1.OdigosCloudConnector) {
	withUID(cr)
	withCollectors(cloudconnectorsv1.CloudConnectorCollectorCapability{
		Type: collectorType, Deployment: true,
	})(cr)
}

func params() DeploymentParams {
	return DeploymentParams{
		Namespace:    "odigos-system",
		WorkloadName: WorkloadName("aws", "aws-prod"),
		Image:        "registry.odigos.io/connector-aws:test",
		SchemaName:   "connector_aws",
	}
}

func portNames(ports []corev1.ContainerPort) map[string]int32 {
	out := map[string]int32{}
	for _, p := range ports {
		out[p.Name] = p.ContainerPort
	}
	return out
}

func servicePortNames(ports []corev1.ServicePort) map[string]int32 {
	out := map[string]int32{}
	for _, p := range ports {
		out[p.Name] = p.Port
	}
	return out
}

// TestOpAMPPlumbingFollowsTheCapability: the port, the label and the policy exist exactly when something
// serves them — the same condition base.Run starts the OpAMP server on. A declared port nothing listens
// on is a port to explain.
//
// Both capability sources are exercised, and so is a list that was refused: a refused list is passed to
// the connector as nothing, so it must produce no plumbing either.
func TestOpAMPPlumbingFollowsTheCapability(t *testing.T) {
	cases := map[string]struct {
		mutate func(*cloudconnectorsv1.OdigosCloudConnector)
		want   bool
	}{
		"from spec.collectors":          {mutate: capabilityFromField, want: true},
		"from the annotation":           {mutate: capabilityFromAnnotation, want: true},
		"no capability at all":          {mutate: noCapability},
		"a capability that was refused": {mutate: refusedCapability},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cr := connector(tc.mutate)
			if got := ManagesCollectors(cr); got != tc.want {
				t.Fatalf("ManagesCollectors = %v, want %v", got, tc.want)
			}

			dep := BuildDeployment(cr, params())
			svc := BuildService(cr, params())
			policy := BuildNetworkPolicy(cr, params(), "")

			container := portNames(dep.Spec.Template.Spec.Containers[0].Ports)
			_, hasContainerPort := container[opamp.ServicePortName]
			_, hasServicePort := servicePortNames(svc.Spec.Ports)[opamp.ServicePortName]
			_, hasWorkloadLabel := dep.Labels[LabelConnectorID]
			_, hasPodLabel := dep.Spec.Template.Labels[LabelConnectorID]
			_, hasServiceLabel := svc.Labels[LabelConnectorID]

			for what, got := range map[string]bool{
				"container port":     hasContainerPort,
				"Service port":       hasServicePort,
				"Deployment label":   hasWorkloadLabel,
				"pod template label": hasPodLabel,
				"Service label":      hasServiceLabel,
				"NetworkPolicy":      policy != nil,
			} {
				if got != tc.want {
					t.Errorf("%s present = %v, want %v", what, got, tc.want)
				}
			}
		})
	}
}

// TestRequiredLabelsCarryTheCRUID: the label IS how the gateway finds this connector's Service. Recomputing
// the workload name in the gateway would be a copy of a rule that lives here, correct until the day it
// was not — with the failure being a Collector routed to a name nothing answers on, while every component
// reports healthy.
func TestRequiredLabelsCarryTheCRUID(t *testing.T) {
	cr := connector(capabilityFromField)

	dep := BuildDeployment(cr, params())
	svc := BuildService(cr, params())

	for what, labels := range map[string]map[string]string{
		"Deployment":   dep.Labels,
		"pod template": dep.Spec.Template.Labels,
		"Service":      svc.Labels,
	} {
		if got := labels[LabelConnectorID]; got != testUID {
			t.Errorf("%s %s = %q, want the CR UID %q", what, LabelConnectorID, got, testUID)
		}
	}

	// The spelling is the gateway's, not this package's: one definition, both consumers.
	if LabelConnectorID != opamp.LabelConnectorID {
		t.Errorf("LabelConnectorID = %q, want the shared %q", LabelConnectorID, opamp.LabelConnectorID)
	}
}

// TestOpAMPPortMatchesTheSharedContract: the port the runtime opens, the port the gateway dials and the
// port the base serves are one number and one name.
func TestOpAMPPortMatchesTheSharedContract(t *testing.T) {
	cr := connector(capabilityFromField)

	if got := portNames(BuildDeployment(cr, params()).Spec.Template.Spec.Containers[0].Ports)[opamp.ServicePortName]; got != opamp.ServerPort {
		t.Errorf("container port = %d, want %d", got, opamp.ServerPort)
	}
	svcPort := servicePortNames(BuildService(cr, params()).Spec.Ports)[opamp.ServicePortName]
	if svcPort != opamp.ServerPort {
		t.Errorf("Service port = %d, want %d", svcPort, opamp.ServerPort)
	}
	// Target port too: a Service port that resolved to a different container port would route OpAMP at
	// the connector's GraphQL server, which would answer and then fail to speak the protocol.
	for _, p := range BuildService(cr, params()).Spec.Ports {
		if p.Name != opamp.ServicePortName {
			continue
		}
		if p.TargetPort.IntValue() != opamp.ServerPort {
			t.Errorf("Service target port = %v, want %d", p.TargetPort, opamp.ServerPort)
		}
	}
}

// TestGatewayOnlyAdmission is the criterion this whole policy exists for. The OpAMP port carries no
// authentication of its own — the gateway authenticated the credential and the identity it asserts is
// what the connector acts on — so this rule is what stands between that identity and anything that could
// forge it.
func TestGatewayOnlyAdmission(t *testing.T) {
	policy := BuildNetworkPolicy(connector(capabilityFromField), params(), "")
	if policy == nil {
		t.Fatal("no NetworkPolicy was built")
	}

	rule := opampRule(t, policy)
	if len(rule.From) != 1 {
		t.Fatalf("the OpAMP rule admits %d peers, want exactly the gateway", len(rule.From))
	}
	peer := rule.From[0]
	if peer.PodSelector == nil {
		t.Fatal("the OpAMP rule does not select pods, so it admits every pod in the namespace")
	}
	if got := peer.PodSelector.MatchLabels[opamp.LabelComponent]; got != opamp.GatewayComponent {
		t.Errorf("the OpAMP rule selects %s=%q, want %q", opamp.LabelComponent, got, opamp.GatewayComponent)
	}
	if peer.IPBlock != nil {
		t.Errorf("the OpAMP rule admits an address range: %+v", peer.IPBlock)
	}
	// Same namespace: an empty NamespaceSelector means EVERY namespace, so it must be absent rather than
	// empty — otherwise a pod carrying the gateway's labels anywhere in the cluster is admitted.
	if peer.NamespaceSelector != nil {
		t.Errorf("the OpAMP rule carries a namespace selector for a gateway in its own namespace: %+v",
			peer.NamespaceSelector)
	}

	// And the policy must select only this connector's pods.
	if got := policy.Spec.PodSelector.MatchLabels[LabelWorkload]; got != params().WorkloadName {
		t.Errorf("the policy selects %s=%q, want this connector's workload %q",
			LabelWorkload, got, params().WorkloadName)
	}
	if len(policy.Spec.PodSelector.MatchLabels) != 1 {
		t.Errorf("the policy selector is %v; the workload label alone is unique per (provider, connector)",
			policy.Spec.PodSelector.MatchLabels)
	}
}

// TestGatewayInAnotherNamespaceIsScoped: a gateway elsewhere needs a namespace selector, and it has to be
// a real one — the empty selector that means "every namespace" is the trap.
func TestGatewayInAnotherNamespaceIsScoped(t *testing.T) {
	policy := BuildNetworkPolicy(connector(capabilityFromField), params(), "odigos-gateway")
	peer := opampRule(t, policy).From[0]

	if peer.NamespaceSelector == nil {
		t.Fatal("a gateway in another namespace was not scoped, so the policy admits it from nowhere")
	}
	if got := peer.NamespaceSelector.MatchLabels[corev1.LabelMetadataName]; got != "odigos-gateway" {
		t.Errorf("namespace selector = %v, want the gateway's namespace", peer.NamespaceSelector.MatchLabels)
	}
	if len(peer.NamespaceSelector.MatchLabels) == 0 {
		t.Error("the namespace selector is empty, which matches every namespace")
	}
}

// TestPolicyRepermitsEverythingItWouldOtherwiseDeny is the trap in NetworkPolicy semantics, and the
// reason this test exists at all.
//
// A pod with no NetworkPolicy accepts everything. The moment one selects it, every port not explicitly
// allowed is DENIED. A policy that mentioned only 4320 would therefore take out the connector's GraphQL
// endpoint and — on CNIs that police node-to-pod traffic — its own readiness and
// liveness probes. The connector would go NotReady for a feature that has nothing to do with either.
func TestPolicyRepermitsEverythingItWouldOtherwiseDeny(t *testing.T) {
	cr := connector(capabilityFromField)
	policy := BuildNetworkPolicy(cr, params(), "")

	allowed := map[int32]bool{}
	for _, rule := range policy.Spec.Ingress {
		for _, p := range rule.Ports {
			allowed[int32(p.Port.IntValue())] = true
		}
	}

	// Only the query endpoint and OpAMP need pod-network access. The admin CLI
	// binds to loopback and is intentionally denied to other pods.
	for name, port := range portNames(BuildDeployment(cr, params()).Spec.Template.Spec.Containers[0].Ports) {
		if port == CLIPort {
			if allowed[port] {
				t.Error("the admin CLI is reachable through the pod network")
			}
			continue
		}
		if !allowed[port] {
			t.Errorf("container port %s (%d) is not allowed by any rule, so this policy closes it", name, port)
		}
	}

	// The probes target the GraphQL port, so closing it would make the connector fail its own health
	// checks — a NetworkPolicy for OpAMP taking the connector down.
	if !allowed[GraphQLPort] {
		t.Errorf("port %d carries the readiness and liveness probes and is not allowed", GraphQLPort)
	}

	// And the re-permitted ports must be open to ANYONE, which is exactly what they were before any
	// policy existed. A From on them would be a new restriction this change did not ask for.
	for _, rule := range policy.Spec.Ingress {
		if isOpAMPRule(rule) {
			continue
		}
		if len(rule.From) != 0 {
			t.Errorf("a re-permit rule restricts its sources to %+v; those ports were open before", rule.From)
		}
	}
}

// TestGeneratedConfigIsRetained: adding OpAMP is purely additive. Everything the connector already had
// is still there, and the immutable pod selector is untouched — a changed selector orphans the running
// Deployment and its live instrumentation.
func TestGeneratedConfigIsRetained(t *testing.T) {
	without := connector(noCapability)
	with := connector(capabilityFromField)

	base := BuildDeployment(without, params())
	extended := BuildDeployment(with, params())

	// Ports: a superset, and the originals unchanged.
	basePorts := portNames(base.Spec.Template.Spec.Containers[0].Ports)
	extPorts := portNames(extended.Spec.Template.Spec.Containers[0].Ports)
	for name, port := range basePorts {
		if extPorts[name] != port {
			t.Errorf("port %s changed from %d to %d", name, port, extPorts[name])
		}
	}
	if len(extPorts) != len(basePorts)+1 {
		t.Errorf("ports went from %v to %v; only OpAMP should be added", basePorts, extPorts)
	}

	// Labels: a superset, and the originals unchanged.
	for k, v := range base.Labels {
		if extended.Labels[k] != v {
			t.Errorf("label %s changed from %q to %q", k, v, extended.Labels[k])
		}
	}

	// The selector is immutable, so it must be identical with and without.
	if base.Spec.Selector.String() != extended.Spec.Selector.String() {
		t.Error("the immutable pod selector changed when the Collector capability was enabled")
	}
	if BuildService(without, params()).Spec.Selector[LabelWorkload] !=
		BuildService(with, params()).Spec.Selector[LabelWorkload] {
		t.Error("the Service selector changed when the Collector capability was enabled")
	}

	// Env is untouched by this change: the endpoint is stamped by its own resolution, not by the port.
	baseEnv := base.Spec.Template.Spec.Containers[0].Env
	extEnv := extended.Spec.Template.Spec.Containers[0].Env
	if len(baseEnv) != len(extEnv) {
		t.Errorf("the env var count changed from %d to %d", len(baseEnv), len(extEnv))
	}
}

// TestAConnectorWithoutCollectorsIsUnchanged: no port to explain, no pod roll, and no NetworkPolicy on a
// pod that never had one. An installation that does not use managed Collectors sees nothing.
func TestAConnectorWithoutCollectorsIsUnchanged(t *testing.T) {
	cr := connector(noCapability)

	ports := portNames(BuildDeployment(cr, params()).Spec.Template.Spec.Containers[0].Ports)
	if len(ports) != 2 || ports[graphQLPortName] != GraphQLPort || ports[cliPortName] != CLIPort {
		t.Errorf("container ports = %v, want only graphql and odictl", ports)
	}
	if svc := servicePortNames(BuildService(cr, params()).Spec.Ports); len(svc) != 1 || svc[graphQLPortName] != GraphQLPort {
		t.Errorf("Service ports = %v, want only graphql", svc)
	}
	if BuildNetworkPolicy(cr, params(), "") != nil {
		t.Error("a connector with no Collector was given a NetworkPolicy, making its pod default-deny")
	}
}

// TestPolicyIsIngressOnly: the connector dials out to Central, to its database and to the cloud provider.
// An egress policy here would be a second thing to maintain and the first thing to break all of that.
func TestPolicyIsIngressOnly(t *testing.T) {
	policy := BuildNetworkPolicy(connector(capabilityFromField), params(), "")

	if len(policy.Spec.PolicyTypes) != 1 || policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("PolicyTypes = %v, want ingress only", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Egress) != 0 {
		t.Errorf("the policy declares egress rules: %+v", policy.Spec.Egress)
	}
}

// TestPolicyIsServerSideApplyable: it is applied with SSA like the Deployment and Service, which needs
// TypeMeta on the object.
func TestPolicyIsServerSideApplyable(t *testing.T) {
	policy := BuildNetworkPolicy(connector(capabilityFromField), params(), "")

	if policy.APIVersion != "networking.k8s.io/v1" || policy.Kind != "NetworkPolicy" {
		t.Errorf("TypeMeta = %s/%s; server-side apply needs it set", policy.APIVersion, policy.Kind)
	}
	if policy.Name != params().WorkloadName || policy.Namespace != params().Namespace {
		t.Errorf("policy is %s/%s, want %s/%s",
			policy.Namespace, policy.Name, params().Namespace, params().WorkloadName)
	}
}

func isOpAMPRule(rule networkingv1.NetworkPolicyIngressRule) bool {
	for _, p := range rule.Ports {
		if int32(p.Port.IntValue()) == opamp.ServerPort {
			return true
		}
	}
	return false
}

func opampRule(t *testing.T, policy *networkingv1.NetworkPolicy) networkingv1.NetworkPolicyIngressRule {
	t.Helper()
	if policy == nil {
		t.Fatal("no NetworkPolicy was built")
	}
	for _, rule := range policy.Spec.Ingress {
		if isOpAMPRule(rule) {
			return rule
		}
	}
	t.Fatalf("no rule mentions the OpAMP port %d", opamp.ServerPort)
	return networkingv1.NetworkPolicyIngressRule{}
}
