package resources

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
)

const (
	awsTypeLambda     = "aws.lambda"
	awsTypeECSCluster = "aws.ecs-cluster"
)

func TestWorkloadNameDeterministicAndBounded(t *testing.T) {
	a := WorkloadName("aws", "prod")
	b := WorkloadName("aws", "prod")
	if a != b {
		t.Errorf("WorkloadName not deterministic: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "odigos-connector-aws-prod-") {
		t.Errorf("unexpected prefix: %q", a)
	}

	long := WorkloadName("aws", strings.Repeat("x", 200))
	if len(long) > 63 {
		t.Errorf("name exceeds 63 chars: %d (%q)", len(long), long)
	}

	// Distinct inputs yield distinct names (hash suffix differentiates).
	if WorkloadName("aws", "prod") == WorkloadName("aws", "staging") {
		t.Error("different connectors produced the same workload name")
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("AWS_Prod.Env"); got != "aws-prod-env" {
		t.Errorf("sanitize = %q, want aws-prod-env", got)
	}
}

func TestInstallationCollectorImageIsStampedOnlyForManagedCollectors(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, image := range []string{"", "registry.example.com/collector:v2"} {
			cr := crFor("prod", "aws", nil)
			if enabled {
				cr.Spec.Collectors = []cloudconnectorsv1.CloudConnectorCollectorCapability{{Type: collectorType, Management: true}}
			}
			dep := BuildDeployment(cr, DeploymentParams{ManagedCollectorImage: image})
			var got string
			for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
				if env.Name == "ODIGOS_COLLECTOR_IMAGE" {
					got = env.Value
				}
			}
			want := ""
			if enabled {
				want = image
			}
			if got != want {
				t.Fatalf("enabled=%v image=%q: stamped %q, want %q", enabled, image, got, want)
			}
		}
	}
}

func crFor(name, provider string, replicas *int32) *cloudconnectorsv1.OdigosCloudConnector {
	return &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "odigos-system"},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider: provider,
			Runtime:  cloudconnectorsv1.CloudConnectorRuntime{Replicas: replicas},
		},
	}
}

// The runtime is not leader-elected: more than one replica would race provider mutations, so the builder
// clamps to MaxReplicas. 0 (intentional disable) and 1 pass through.
func TestReplicasForClampsAboveMax(t *testing.T) {
	cases := []struct {
		name    string
		in      *int32
		want    int32
		clamped bool
	}{
		{"nil defaults to 1", nil, 1, false},
		{"one", ptr.To(int32(1)), 1, false},
		{"zero disable", ptr.To(int32(0)), 0, false},
		{"three clamped", ptr.To(int32(3)), MaxReplicas, true},
	}
	for _, tc := range cases {
		got, clamped := ReplicasFor(crFor("c", "aws", tc.in))
		if got != tc.want || clamped != tc.clamped {
			t.Errorf("%s: ReplicasFor = (%d,%v), want (%d,%v)", tc.name, got, clamped, tc.want, tc.clamped)
		}
	}
	dep := BuildDeployment(crFor("c", "aws", ptr.To(int32(5))), DeploymentParams{WorkloadName: "w"})
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != MaxReplicas {
		t.Errorf("deployment replicas = %v, want %d", dep.Spec.Replicas, MaxReplicas)
	}
}

// The Deployment/Service select ONLY on the unique, bounded workload label — never the raw connector
// name — so a Service can't fan out to a different provider's connector or a stale Deployment.
func TestSelectorsUseBoundedWorkloadLabel(t *testing.T) {
	cr := crFor("my-connector", "aws", nil)
	p := DeploymentParams{WorkloadName: WorkloadName(cr.Spec.Provider, cr.Name), Namespace: "odigos-system"}

	dep := BuildDeployment(cr, p)
	sel := dep.Spec.Selector.MatchLabels
	if len(sel) != 1 || sel[LabelWorkload] != p.WorkloadName {
		t.Errorf("deployment selector = %v, want only {%s: %s}", sel, LabelWorkload, p.WorkloadName)
	}
	if _, ok := sel[LabelCloudConnector]; ok {
		t.Errorf("selector must not include the unbounded connector-name label: %v", sel)
	}
	if dep.Spec.Template.Labels[LabelWorkload] != p.WorkloadName {
		t.Errorf("pod template missing %s label", LabelWorkload)
	}

	svc := BuildService(cr, p)
	if len(svc.Spec.Selector) != 1 || svc.Spec.Selector[LabelWorkload] != p.WorkloadName {
		t.Errorf("service selector = %v, want only {%s: %s}", svc.Spec.Selector, LabelWorkload, p.WorkloadName)
	}
}

// A connector name over the 63-char label limit must be truncated to a valid label value (informational
// only), while the selector stays on the bounded workload hash.
func TestLabelValueBoundsLongName(t *testing.T) {
	got := labelValue(strings.Repeat("a", 100))
	if len(got) > 63 {
		t.Errorf("labelValue length = %d, want <= 63", len(got))
	}
}

// TestBuildDeploymentCredentialEnvFromSpec asserts the connector Deployment wires credentials as Secret
// keyRefs derived from the AWS connector spec's auth definition (field name -> Secret key, field envVar
// -> env var) rather than hardcoded — so adding a provider needs no controller change.
func TestBuildDeploymentCredentialEnvFromSpec(t *testing.T) {
	cr := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "odigos-central"},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider:             "aws",
			BootstrapRegion:      "eu-west-1",
			Account:              cloudconnectorsv1.CloudConnectorAccount{ID: "123456789012"},
			CredentialsSecretRef: cloudconnectorsv1.CloudConnectorCredentialsSecretRef{Name: "prod-creds"},
			Workloads: []cloudconnectorsv1.CloudConnectorWorkloadCapability{
				{Type: awsTypeLambda, Discovery: true},
			},
			ComputePlatforms: []cloudconnectorsv1.CloudConnectorComputePlatformCapability{
				{Type: awsTypeECSCluster, Discovery: true},
			},
		},
	}
	dep := BuildDeployment(cr, DeploymentParams{
		Namespace: "odigos-central", WorkloadName: "w", Image: "img",
		DBSecretName: "db", DBSecretKey: "dsn", SchemaName: "connector_aws",
	})

	env := dep.Spec.Template.Spec.Containers[0].Env
	byName := make(map[string]string) // env var name -> secret key
	plain := make(map[string]string)  // env var name -> literal value
	for _, e := range env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "prod-creds" {
			byName[e.Name] = e.ValueFrom.SecretKeyRef.Key
		} else {
			plain[e.Name] = e.Value
		}
	}

	want := map[string]string{
		"AWS_ACCESS_KEY_ID":      "accessKeyId",
		"AWS_SECRET_ACCESS_KEY":  "secretAccessKey",
		"AWS_SESSION_TOKEN":      "sessionToken",
		"ODIGOS_AWS_ROLE_ARN":    "roleArn",
		"ODIGOS_AWS_EXTERNAL_ID": "externalId",
	}
	for envName, secretKey := range want {
		if got, ok := byName[envName]; !ok {
			t.Errorf("missing credential env %q (secretRef into prod-creds)", envName)
		} else if got != secretKey {
			t.Errorf("env %q secret key = %q, want %q", envName, got, secretKey)
		}
	}

	// Capabilities are stamped as JSON, split by domain (workloads/computePlatforms); regions and
	// permission mode are gone.
	if plain["ODIGOS_BOOTSTRAP_REGION"] != "eu-west-1" {
		t.Fatalf("bootstrap region not forwarded: %q", plain["ODIGOS_BOOTSTRAP_REGION"])
	}
	caps := plain["ODIGOS_CAPABILITIES"]
	for _, want := range []string{`"workloads":`, `"computePlatforms":`, `"type":"` + awsTypeLambda + `"`, `"type":"` + awsTypeECSCluster + `"`, `"discovery":true`} {
		if !strings.Contains(caps, want) {
			t.Errorf("ODIGOS_CAPABILITIES = %q, want it to contain %q", caps, want)
		}
	}
	if _, ok := plain["ODIGOS_AWS_REGIONS"]; ok {
		t.Error("ODIGOS_AWS_REGIONS should no longer be set (all enabled regions are scanned)")
	}
	if _, ok := plain["ODIGOS_PERMISSION_MODE"]; ok {
		t.Error("ODIGOS_PERMISSION_MODE should no longer be set (replaced by capabilities)")
	}
}

// TestConnectorGetsTheOnPremToken pins how the install's enterprise licence token reaches a connector,
// because a managed Collector cannot start without one: has the connector
// copy THIS install's odigos-central value into the customer's own secret store, so no Odigos-vendor
// licence material enters a customer account and no token crosses between installs.
//
// Three properties, and each has a distinct failure:
//
//   - the env var name, which must match collectorimage.EnvOnPremToken (a different module, so the
//     compiler cannot check it) and the name the Collector's auth extension reads;
//   - the source, which must be the odigos-central Secret every other enterprise component reads, not a
//     value inlined into the pod spec where anyone with pod read access could see it;
//   - Optional, because a community install has no odigos-central token and a connector that
//     cannot manage a Collector must still start and run its other two domains — a required keyRef
//     would leave the pod stuck in CreateContainerConfigError with nothing to explain it.
func TestConnectorGetsTheOnPremToken(t *testing.T) {
	cr := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "odigos-central"},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider:             "aws",
			Account:              cloudconnectorsv1.CloudConnectorAccount{ID: "123456789012"},
			CredentialsSecretRef: cloudconnectorsv1.CloudConnectorCredentialsSecretRef{Name: "prod-creds"},
		},
	}
	dep := BuildDeployment(cr, DeploymentParams{
		Namespace: "odigos-central", WorkloadName: "w", Image: "img",
		DBSecretName: "db", DBSecretKey: "dsn", SchemaName: "connector_aws",
	})

	var found *corev1.EnvVar
	for i, e := range dep.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "ODIGOS_ONPREM_TOKEN" {
			found = &dep.Spec.Template.Spec.Containers[0].Env[i]
		}
	}
	if found == nil {
		t.Fatal("the connector Deployment carries no ODIGOS_ONPREM_TOKEN; a managed Collector would have no licence to copy into the customer's account and would refuse to start")
	}
	if found.Value != "" {
		t.Errorf("ODIGOS_ONPREM_TOKEN is stamped as a literal value; it must be a Secret keyRef — a plain value is readable by anyone who can read the pod spec")
	}
	if found.ValueFrom == nil || found.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("ODIGOS_ONPREM_TOKEN does not come from a Secret: %+v", found.ValueFrom)
	}
	ref := found.ValueFrom.SecretKeyRef
	if ref.Name != centralSecretName || ref.Key != centralSecretOnPremTokenKey {
		t.Errorf("ODIGOS_ONPREM_TOKEN reads %s/%s, want %s/%s (the secret every other enterprise component uses)",
			ref.Name, ref.Key, centralSecretName, centralSecretOnPremTokenKey)
	}
	if ref.Optional == nil || !*ref.Optional {
		t.Error("ODIGOS_ONPREM_TOKEN is a required keyRef; a community install has no odigos-central token and the connector pod would never start")
	}
}
