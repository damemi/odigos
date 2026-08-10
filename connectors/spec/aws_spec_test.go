package spec

import (
	"strings"
	"testing"
)

func TestAWSSpecLoads(t *testing.T) {
	s, ok := Get("aws")
	if !ok {
		t.Fatal("aws spec should be registered from spec/data/aws.yaml")
	}
	if s.Provider != "aws" {
		t.Fatalf("provider = %q, want aws", s.Provider)
	}

	disc := s.Workloads.Discovery
	if !disc.Enabled {
		t.Error("workload discovery feature should be enabled in P0")
	}
	if !containsStr(disc.Permissions, "ec2:DescribeRegions") {
		t.Errorf("workload discovery baseline should include ec2:DescribeRegions, got %v", disc.Permissions)
	}
	if !s.ComputePlatforms.Discovery.Enabled {
		t.Error("compute-platform discovery feature should be enabled")
	}
	if !containsStr(s.ComputePlatforms.Discovery.Permissions, "ec2:DescribeRegions") {
		t.Errorf("compute-platform discovery baseline should include ec2:DescribeRegions, got %v", s.ComputePlatforms.Discovery.Permissions)
	}
	if !containsStr(s.Auth.Permissions, "sts:GetCallerIdentity") {
		t.Errorf("auth baseline should include sts:GetCallerIdentity, got %v", s.Auth.Permissions)
	}
	if len(s.ConnectorFields()) != 1 {
		t.Errorf("connector fields = %d, want 1 (accountId)", len(s.ConnectorFields()))
	}
	methods := s.AuthMethods()
	if len(methods) != 3 {
		t.Fatalf("auth methods = %d, want 3", len(methods))
	}
	staticKeys, ok := s.AuthMethodByID("static-keys")
	if !ok || len(staticKeys.Fields) != 3 {
		t.Errorf("static-keys method should declare 3 fields, got %v", staticKeys.Fields)
	}
	assumeRole, ok := s.AuthMethodByID("assume-role")
	if !ok || len(assumeRole.Fields) != 2 {
		t.Errorf("assume-role method should declare 2 fields, got %v", assumeRole.Fields)
	}
	if len(assumeRole.BaseMethods) != 2 {
		t.Errorf("assume-role baseMethods = %v, want [workload-identity static-keys]", assumeRole.BaseMethods)
	}
	credFields := s.CredentialFields()
	wantEnv := map[string]string{
		"accessKeyId":     "AWS_ACCESS_KEY_ID",
		"secretAccessKey": "AWS_SECRET_ACCESS_KEY",
		"sessionToken":    "AWS_SESSION_TOKEN",
		"roleArn":         "ODIGOS_AWS_ROLE_ARN",
		"externalId":      "ODIGOS_AWS_EXTERNAL_ID",
	}
	if len(credFields) != len(wantEnv) {
		t.Fatalf("credential fields = %d, want %d", len(credFields), len(wantEnv))
	}
	for _, f := range credFields {
		if want, ok := wantEnv[f.Name]; !ok || f.EnvVar != want {
			t.Errorf("credential field %q envVar = %q, want %q", f.Name, f.EnvVar, want)
		}
	}
	// Workload discovery declares the directly-instrumentable types; compute-platform discovery declares
	// the managed environments. The two domains share no type names.
	wantWorkloadDisc := []string{"aws.lambda", "aws.fargate-task"}
	for _, name := range wantWorkloadDisc {
		if a := s.Workloads.Discovery.PermissionsForType(name); len(a) == 0 {
			t.Errorf("workload discovery type %q should declare permissions", name)
		}
	}
	wantCPDisc := []string{"aws.ecs-cluster", "aws.fargate-cluster", "aws.eks", "aws.ec2"}
	for _, name := range wantCPDisc {
		if a := s.ComputePlatforms.Discovery.PermissionsForType(name); len(a) == 0 {
			t.Errorf("compute-platform discovery type %q should declare permissions", name)
		}
	}
	// Both ECS cluster types are served by one detector whose Fargate classification issues a
	// launch-type-filtered ecs:ListTasks per cluster; a denial fails the whole region scan, so neither
	// type may omit it.
	for _, name := range []string{"aws.ecs-cluster", "aws.fargate-cluster"} {
		if p := s.ComputePlatforms.Discovery.PermissionsForType(name); !containsStr(p, "ecs:ListTasks") {
			t.Errorf("compute-platform discovery type %q permissions %v missing ecs:ListTasks", name, p)
		}
	}
	// Workload discovery reads cluster ARNs from ListClusters only — describing the cluster belongs to
	// the compute-platform domain, so declaring it here would over-grant.
	if p := s.Workloads.Discovery.PermissionsForType("aws.fargate-task"); containsStr(p, "ecs:DescribeClusters") {
		t.Errorf("aws.fargate-task workload discovery should not require ecs:DescribeClusters, got %v", p)
	}

	wantInstrMethod := map[string]string{
		"aws.lambda":       MethodService,
		"aws.fargate-task": MethodSidecar,
	}
	for name, method := range wantInstrMethod {
		if got := s.Workloads.Instrumentation.MethodForType(name); got != method {
			t.Errorf("instrumentation type %q method = %q, want %q", name, got, method)
		}
		if a := s.Workloads.Instrumentation.PermissionsForType(name); len(a) == 0 {
			t.Errorf("instrumentation type %q should declare permissions", name)
		}
	}

	// Compute platforms are discovery-only for now: installing an agent onto them is the installation
	// feature, which is not supported yet and declares no types.
	install := s.ComputePlatforms.Installation
	if install.Enabled {
		t.Error("installation feature should be disabled (not supported yet)")
	}
	if len(install.Types) != 0 {
		t.Errorf("installation feature should declare no types, got %v", install.Types)
	}
	// A compute-platform type is not a workload instrumentation type.
	if got := s.Workloads.Instrumentation.MethodForType("aws.ecs-cluster"); got != "" {
		t.Errorf("aws.ecs-cluster should not be an instrumentation type, got method %q", got)
	}
}

func TestAWSCapabilityResolution(t *testing.T) {
	s := MustGet("aws")

	caps := Capabilities{
		Workloads: []WorkloadCapability{
			{Type: "aws.lambda", Discovery: true, Instrumentation: true},
			{Type: "aws.fargate-task", Discovery: true, Instrumentation: true},
		},
		ComputePlatforms: []ComputePlatformCapability{
			{Type: "aws.ecs-cluster", Discovery: true},
			{Type: "aws.fargate-cluster", Discovery: true},
			{Type: "aws.eks", Discovery: true},
			{Type: "aws.ec2", Discovery: true},
		},
	}

	disc := s.ActiveDiscoveryTypes(caps)
	if len(disc) != 6 {
		t.Errorf("active discovery types = %d, want 6", len(disc))
	}
	if got := s.ActiveWorkloadDiscoveryTypes(caps); len(got) != 2 {
		t.Errorf("active workload discovery types = %v, want 2 (aws.lambda, aws.fargate-task)", got)
	}
	if got := s.ActiveComputePlatformDiscoveryTypes(caps); len(got) != 4 {
		t.Errorf("active compute-platform discovery types = %v, want 4", got)
	}

	instr := s.ActiveInstrumentationTypes(caps)
	wantInstr := map[string]bool{"aws.lambda": true, "aws.fargate-task": true}
	if len(instr) != len(wantInstr) {
		t.Errorf("active instrumentation types = %v, want aws.lambda and aws.fargate-task", instr)
	}
	for _, ty := range instr {
		if !wantInstr[ty.Name] {
			t.Errorf("unexpected active instrumentation type %q", ty.Name)
		}
	}
	// Installation is not supported yet (empty feature), so no compute platform is actively managed.
	if cp := s.ActiveComputePlatformTypes(caps); len(cp) != 0 {
		t.Errorf("active compute-platform types = %v, want none (installation not supported)", cp)
	}

	// Both workload instrumentation kinds are implemented; nothing should be reported unimplemented.
	if unimpl := s.UnimplementedInstrumentationKinds(); len(unimpl) != 0 {
		t.Errorf("unimplemented instrumentation kinds = %v, want none", unimpl)
	}
	// Every compute-platform discovery kind is unimplemented while installation is unsupported.
	unimplCP := s.UnimplementedComputePlatformKinds()
	for _, name := range []string{"aws.ecs-cluster", "aws.fargate-cluster", "aws.eks", "aws.ec2"} {
		if !unimplCP[name] {
			t.Errorf("expected %s to be reported as an unimplemented compute-platform kind", name)
		}
	}

	perms := s.RequiredPermissions(caps)
	for _, want := range []string{
		"sts:GetCallerIdentity", "ec2:DescribeRegions", "lambda:ListFunctions", "ecs:ListClusters",
		// Tag-filtered Lambda discovery issues one ListTags per function; a denial aborts the scan.
		"lambda:ListTags",
		// Fargate instrumentation write path, the pass-role it implies, and the tag-on-create
		// authorization RegisterTaskDefinition needs when it carries the task definition's tags.
		"ecs:RegisterTaskDefinition", "ecs:UpdateService", "iam:PassRole", "ecs:TagResource",
	} {
		if !containsStr(perms, want) {
			t.Errorf("required permissions %v missing %q", perms, want)
		}
	}

	if got := s.ActiveDiscoveryTypes(Capabilities{}); got != nil {
		t.Errorf("active discovery types for no caps = %v, want nil", got)
	}
	if containsStr(s.RequiredPermissions(Capabilities{}), "ec2:DescribeRegions") {
		t.Error("region enumeration permission should not be required when no discovery cap is active")
	}
}

func TestAWSAuthResolveAllModes(t *testing.T) {
	auth := MustGet("aws").Auth

	chainIDs := func(creds map[string]string) ([]string, error) {
		chain, err := auth.Resolve(creds)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(chain))
		for i, m := range chain {
			ids[i] = m.ID
		}
		return ids, nil
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	ok := []struct {
		name  string
		creds map[string]string
		want  []string
	}{
		{"workload-identity (no creds)", nil, []string{"workload-identity"}},
		{"static-keys", map[string]string{"accessKeyId": "AKIA", "secretAccessKey": "s"}, []string{"static-keys"}},
		{"static-keys + session token", map[string]string{"accessKeyId": "AKIA", "secretAccessKey": "s", "sessionToken": "t"}, []string{"static-keys"}},
		{"assume-role on workload-identity", map[string]string{"roleArn": "arn:aws:iam::1:role/r"}, []string{"workload-identity", "assume-role"}},
		{"assume-role on static-keys", map[string]string{"roleArn": "arn:aws:iam::1:role/r", "accessKeyId": "AKIA", "secretAccessKey": "s"}, []string{"static-keys", "assume-role"}},
	}
	for _, tc := range ok {
		got, err := chainIDs(tc.creds)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if !eq(got, tc.want) {
			t.Errorf("%s: chain = %v, want %v", tc.name, got, tc.want)
		}
	}

	bad := []struct {
		name  string
		creds map[string]string
	}{
		{"static-keys missing secret", map[string]string{"accessKeyId": "AKIA"}},
		{"session token alone", map[string]string{"sessionToken": "t"}},
		{"unknown field", map[string]string{"projectId": "x"}},
	}
	for _, tc := range bad {
		if _, err := auth.Resolve(tc.creds); err == nil {
			t.Errorf("%s: expected an error, got none", tc.name)
		}
	}
}

func TestAWSCreateRoleArtifacts(t *testing.T) {
	s := MustGet("aws")

	if s.Auth.DefaultMethod != "assume-role" {
		t.Errorf("auth defaultMethod = %q, want assume-role", s.Auth.DefaultMethod)
	}

	if s.Setup.Subtitle == "" {
		t.Error("setup.subtitle should be set for the step-2 header")
	}

	arts := s.SetupArtifactsForSection("create-role")
	wantVariants := []string{"cloudformation", "terraform", "aws-cli", "manual", "iam-user"}
	if len(arts) != len(wantVariants) {
		t.Fatalf("create-role setup artifacts = %d, want %d (%v)", len(arts), len(wantVariants), wantVariants)
	}

	byVariant := map[string]SetupArtifact{}
	for i, want := range wantVariants {
		a := arts[i]
		if a.Variant != want {
			t.Errorf("artifact[%d].variant = %q, want %q", i, a.Variant, want)
		}
		if a.VariantLabel == "" {
			t.Errorf("artifact[%d] (%s) missing variantLabel", i, want)
		}
		if len(a.Steps) == 0 {
			t.Errorf("artifact[%d] (%s) should declare steps", i, want)
		}
		if a.Template == "" {
			t.Errorf("artifact[%d] (%s) should declare a template", i, want)
		}
		byVariant[a.Variant] = a
	}

	// Each create-role variant must actually CREATE the role it promises — a read-only snippet (the
	// original aws-cli artifact only ran `aws iam get-role`) leaves the user with nothing to paste.
	// The iam-user variant is the access-keys path: it creates a user, not a role.
	wantCreates := map[string]string{
		"cloudformation": "AWS::IAM::Role",
		"terraform":      `resource "aws_iam_role"`,
		"aws-cli":        "aws iam create-role",
		"manual":         "sts:AssumeRole",
		"iam-user":       "aws iam create-user",
	}
	for variant, want := range wantCreates {
		if !strings.Contains(byVariant[variant].Template, want) {
			t.Errorf("%s create-role template should contain %q, got:\n%s", variant, want, byVariant[variant].Template)
		}
	}

	// Every trust-policy-bearing variant interpolates the form's externalId, so the role Odigos is told
	// to assume and the external id Odigos actually sends cannot drift apart.
	for _, variant := range []string{"cloudformation", "terraform", "aws-cli", "manual"} {
		if !strings.Contains(byVariant[variant].Template, "{{externalId}}") {
			t.Errorf("%s create-role template should carry {{externalId}}, got:\n%s", variant, byVariant[variant].Template)
		}
	}

	// A template that embeds the permission list must declare how to render one item; without an
	// itemTemplate the renderer falls back to a bare permission string, which is invalid in both the
	// CloudFormation YAML list and the Terraform string list.
	for _, a := range append(arts, s.SetupArtifactsForSection("permissions")...) {
		if strings.Contains(a.Template, "{{permissions}}") && a.ItemTemplate == "" {
			t.Errorf("artifact %q embeds {{permissions}} but declares no itemTemplate", a.ID)
		}
	}

	creds := s.SetupArtifactsForSection("credentials")
	if len(creds) != 1 || creds[0].Title == "" {
		t.Errorf("credentials section artifact should declare a title, got %+v", creds)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
