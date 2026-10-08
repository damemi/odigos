package spec

import (
	"strings"
	"testing"
)

const awsCollectorType = "aws.fargate-collector"

// TestAWSCollectorsDomainLoads pins the AWS declaration: both features enabled, the type declared on
// both, and the deployment method that tells a provider deployer which kind of workload to create.
func TestAWSCollectorsDomainLoads(t *testing.T) {
	s := MustGet("aws")

	if !s.Collectors.Management.Enabled {
		t.Error("collectors.management should be enabled for AWS")
	}
	if !s.Collectors.Deployment.Enabled {
		t.Error("collectors.deployment should be enabled for AWS")
	}
	if !containsStr(s.Collectors.Management.Permissions, "ec2:DescribeRegions") {
		t.Errorf("collectors.management baseline should enumerate regions, got %v", s.Collectors.Management.Permissions)
	}
	mgmt, ok := s.Collectors.Management.findType(awsCollectorType)
	if !ok {
		t.Fatalf("collectors.management does not declare %s", awsCollectorType)
	}
	if mgmt.Method != "" {
		t.Errorf("a management type must declare no method, got %q", mgmt.Method)
	}
	for _, permission := range []string{
		"ec2:DescribeVpcs",
		"ec2:DescribeRouteTables",
		"ec2:DescribeVpcPeeringConnections",
		"ec2:DescribeNetworkAcls",
		"ec2:DescribeSecurityGroups",
	} {
		if !containsStr(mgmt.Permissions, permission) {
			t.Errorf("collectors.management for %s cannot validate regional reachability: missing %s", awsCollectorType, permission)
		}
	}
	dep, ok := s.Collectors.Deployment.findType(awsCollectorType)
	if !ok {
		t.Fatalf("collectors.deployment does not declare %s", awsCollectorType)
	}
	if dep.Method != MethodFargateService {
		t.Errorf("deployment method = %q, want %q", dep.Method, MethodFargateService)
	}
}

// TestAWSManualModeCollectorHasNoWriteVerb is the observe-only criterion asserted against the REAL spec
// rather than a fixture. It generalizes past whatever permissions happen to be declared today: any
// write-capable permission added anywhere the manual-mode aggregation reaches fails here, which is what
// keeps a customer who deploys their own Collector from being asked for create/delete grants.
func TestAWSManualModeCollectorHasNoWriteVerb(t *testing.T) {
	for _, provider := range Providers() {
		s := MustGet(provider)
		for _, mt := range s.Collectors.Management.Types {
			caps := Capabilities{Collectors: []CollectorCapability{{Type: mt.Name, Management: true}}}
			for _, p := range s.RequiredPermissions(caps) {
				for _, verb := range writeVerbs {
					if strings.Contains(p, verb) {
						t.Errorf("%s/%s manual mode aggregates write-capable permission %q (matched %q)",
							provider, mt.Name, p, verb)
					}
				}
			}
		}
	}
}

// TestAWSAutomaticModeCollectorAddsOnlyDeploymentPermissions: the difference between the two modes must
// be exactly the deployment type's permissions. Anything else means a mutating grant leaked onto a
// management type, where manual mode would silently inherit it.
func TestAWSAutomaticModeCollectorAddsOnlyDeploymentPermissions(t *testing.T) {
	s := MustGet("aws")
	manual := s.RequiredPermissions(Capabilities{Collectors: []CollectorCapability{
		{Type: awsCollectorType, Management: true},
	}})
	auto := s.RequiredPermissions(Capabilities{Collectors: []CollectorCapability{
		{Type: awsCollectorType, Management: true, Deployment: true},
	}})

	inManual := map[string]bool{}
	for _, p := range manual {
		inManual[p] = true
	}
	added := map[string]bool{}
	for _, p := range auto {
		if !inManual[p] {
			added[p] = true
		}
	}

	dep, ok := s.Collectors.Deployment.findType(awsCollectorType)
	if !ok {
		t.Fatalf("collectors.deployment does not declare %s", awsCollectorType)
	}
	declared := map[string]bool{}
	for _, p := range dep.Permissions {
		declared[p] = true
	}
	for p := range added {
		if !declared[p] {
			t.Errorf("automatic mode adds %q, which the deployment type does not declare", p)
		}
	}
	for p := range declared {
		// A deployment permission that is already in the manual set is a read the management type also
		// needs — legitimate, and not something to fail on. Anything else must have been added.
		if !added[p] && !inManual[p] {
			t.Errorf("deployment declares %q but automatic mode does not require it", p)
		}
	}
	if len(added) == 0 {
		t.Error("automatic mode requires no permission manual mode does not; the deployment set is not being aggregated")
	}
}

// TestAWSCollectorDeploymentCoversTheDeclaredTopology: each resource the deployer creates needs a
// permission to create it. A missing one is not caught by any parse check — it surfaces as an
// authorization failure partway through building a customer's Collector, leaving a half-built stack.
func TestAWSCollectorDeploymentCoversTheDeclaredTopology(t *testing.T) {
	s := MustGet("aws")
	dep, ok := s.Collectors.Deployment.findType(awsCollectorType)
	if !ok {
		t.Fatalf("collectors.deployment does not declare %s", awsCollectorType)
	}
	// One representative create per object in the Collector's topology, plus the teardown counterpart for
	// each — a create without a delete is infrastructure Odigos can build and never remove.
	for _, want := range []string{
		"ecs:CreateService", "ecs:DeleteService",
		"ecs:RegisterTaskDefinition",
		"elasticloadbalancing:CreateLoadBalancer", "elasticloadbalancing:DeleteLoadBalancer",
		"elasticloadbalancing:CreateTargetGroup", "elasticloadbalancing:DeleteTargetGroup",
		"ec2:CreateSecurityGroup", "ec2:DeleteSecurityGroup",
		"logs:CreateLogGroup", "logs:DeleteLogGroup",
		"secretsmanager:CreateSecret", "secretsmanager:DeleteSecret",
	} {
		if !containsStr(dep.Permissions, want) {
			t.Errorf("collectors.deployment for %s does not declare %s", awsCollectorType, want)
		}
	}
	// Revoke as well as authorize: narrowing a widened ingress rule back down once a workload's own
	// security group resolves is a revoke, and without it the rule stays wide for ever.
	if !containsStr(dep.Permissions, "ec2:RevokeSecurityGroupIngress") {
		t.Error("collectors.deployment cannot revoke an ingress rule, so a widened one could never be narrowed")
	}
	// Ownership tagging is the only licence teardown has to remove an object, so it is required.
	if !containsStr(dep.Permissions, "ecs:TagResource") {
		t.Error("collectors.deployment cannot tag, so nothing would separate an Odigos-created object from the customer's")
	}
}

// TestAWSCollectorDeploymentNeverMutatesTheCluster pins the pre-provisioned-cluster boundary. The
// connector may validate the cluster named by clusterArn, but installing or removing a Collector must
// not require authority over the cluster itself.
func TestAWSCollectorDeploymentNeverMutatesTheCluster(t *testing.T) {
	s := MustGet("aws")
	dep, ok := s.Collectors.Deployment.findType(awsCollectorType)
	if !ok {
		t.Fatalf("collectors.deployment does not declare %s", awsCollectorType)
	}
	if !containsStr(dep.Permissions, "ecs:DescribeClusters") {
		t.Error("collectors.deployment cannot validate the customer-provisioned ECS cluster")
	}
	for _, forbidden := range []string{
		"ecs:CreateCluster",
		"ecs:DeleteCluster",
		"ecs:PutClusterCapacityProviders",
		"ecs:DeregisterTaskDefinition",
		"ecs:ListTagsForResource",
		"elasticloadbalancing:DeleteListener",
		"application-autoscaling:DeleteScalingPolicy",
		"iam:CreateRole",
		"iam:DeleteRole",
		"iam:GetRole",
		"iam:PutRolePolicy",
		"iam:DeleteRolePolicy",
		"iam:TagRole",
		"iam:CreateServiceLinkedRole",
	} {
		if containsStr(dep.Permissions, forbidden) {
			t.Errorf("collectors.deployment still requires cluster mutation permission %s", forbidden)
		}
	}
	if !containsStr(dep.Permissions, "iam:PassRole") {
		t.Error("collectors.deployment cannot pass the configured task execution role to ECS")
	}
}

func TestAWSCollectorExecutionRoleInstructionsArePublished(t *testing.T) {
	s := MustGet("aws")
	artifacts := s.CollectorArtifacts(awsCollectorType)
	if len(artifacts) == 0 {
		t.Fatal("AWS Collector has no prerequisite instructions")
	}
	text := ""
	for _, artifact := range artifacts {
		text += artifact.Description + "\n" + strings.Join(artifact.Steps, "\n") + "\n" + artifact.Template
	}
	for _, want := range []string{
		"executionRoleArn",
		"ecs-tasks.amazonaws.com",
		"secretsmanager:GetSecretValue",
		"logs:PutLogEvents",
		"iam:PassedToService",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Collector prerequisite instructions do not mention %q", want)
		}
	}
}

// TestAWSCollectorScalingIsFullyGranted: automatic scaling is a whole second API, and every one of these
// is reached on a path a customer will actually take.
//
// The mode SWITCH is what makes the set larger than it looks. Registering a target needs the register
// and the policy put; switching back to manual needs the deregister; and telling a target Odigos
// registered from one the customer's own template did needs ListTagsForResource, because a describe
// returns no tags. Without that last one Odigos can prove nothing about the target and therefore removes
// nothing — safe, but a Collector that never cleans up after a mode change.
func TestAWSCollectorScalingIsFullyGranted(t *testing.T) {
	s := MustGet("aws")
	dep, ok := s.Collectors.Deployment.findType(awsCollectorType)
	if !ok {
		t.Fatalf("collectors.deployment does not declare %s", awsCollectorType)
	}
	for _, want := range []string{
		"application-autoscaling:RegisterScalableTarget",
		"application-autoscaling:DeregisterScalableTarget",
		"application-autoscaling:PutScalingPolicy",
		"application-autoscaling:DescribeScalableTargets",
		"application-autoscaling:DescribeScalingPolicies",
		"application-autoscaling:ListTagsForResource",
		// The first RegisterScalableTarget in an account creates the service-linked role Application
		// Target tracking's alarms, per AWS's own documented policy for configuring it.
		"cloudwatch:PutMetricAlarm",
		"cloudwatch:DeleteAlarms",
		"cloudwatch:DescribeAlarms",
	} {
		if !containsStr(dep.Permissions, want) {
			t.Errorf("collectors.deployment for %s does not declare %s", awsCollectorType, want)
		}
	}
	// And none of it reaches manual mode: a customer who deploys their own Collector configures their own
	// scaling, so being asked to grant an autoscaling write would be asking for access to something
	// Odigos has no business touching.
	manual := s.RequiredPermissions(Capabilities{Collectors: []CollectorCapability{
		{Type: awsCollectorType, Management: true},
	}})
	for _, p := range manual {
		if strings.HasPrefix(p, "application-autoscaling:") || strings.HasPrefix(p, "cloudwatch:") {
			t.Errorf("manual mode requires %q", p)
		}
	}
}

// TestAWSCollectorManagementCannotReadItsOwnSecrets: the connector writes the credential and licence
// secrets and never reads them back — that is what keeps the only copy of a credential out of the
// connector's memory after it is installed. GetSecretValue belongs to the task execution role the
// Collector runs with, which the customer pre-creates, not to the connector's own credentials.
func TestAWSCollectorManagementCannotReadItsOwnSecrets(t *testing.T) {
	s := MustGet("aws")
	auto := s.RequiredPermissions(Capabilities{Collectors: []CollectorCapability{
		{Type: awsCollectorType, Management: true, Deployment: true},
	}})
	if containsStr(auto, "secretsmanager:GetSecretValue") {
		t.Errorf("the connector requires secretsmanager:GetSecretValue; it writes credentials and never reads them back: %v", auto)
	}
}

// TestAWSCollectorCapabilityIsHonorable: the real spec must be able to honor the capability set a customer
// would actually be given, in both modes, with no warnings — the same standard
// TestRealProviderCapabilitiesAreHonorable holds the other domains to.
func TestAWSCollectorCapabilityIsHonorable(t *testing.T) {
	s := MustGet("aws")
	for name, caps := range map[string]Capabilities{
		"manual": {Collectors: []CollectorCapability{{Type: awsCollectorType, Management: true}}},
		"automatic": {Collectors: []CollectorCapability{
			{Type: awsCollectorType, Management: true, Deployment: true},
		}},
	} {
		if err := caps.Validate(); err != nil {
			t.Errorf("%s: Validate = %v", name, err)
		}
		if warnings := s.UnsupportedCapabilities(caps); len(warnings) != 0 {
			t.Errorf("%s: the AWS spec cannot honor its own capability set: %v", name, warnings)
		}
		if got := typeNames(s.CollectorTypes(caps)); len(got) != 1 || got[0] != awsCollectorType {
			t.Errorf("%s: CollectorTypes = %v", name, got)
		}
	}
}

// TestNoProviderDeclaresACollectorItCannotConfigure: every deployment type must have a management type,
// which parse already enforces — asserted here across the real specs so a provider added later cannot
// ship a deployer with no config plane, which would build a Collector serving the inert fallback for ever.
func TestNoProviderDeclaresACollectorItCannotConfigure(t *testing.T) {
	for _, provider := range Providers() {
		s := MustGet(provider)
		for _, dt := range s.Collectors.Deployment.Types {
			if _, ok := s.Collectors.Management.findType(dt.Name); !ok {
				t.Errorf("%s declares a deployment for %q with no management counterpart", provider, dt.Name)
			}
			if dt.Method == "" {
				t.Errorf("%s deployment type %q declares no method", provider, dt.Name)
			}
		}
	}
}
