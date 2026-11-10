package spec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every AWS SDK Input used by Collector runtime code requires its matching IAM
// action in automatic mode. This catches permission drift when a new Describe
// or write call is added without changing the setup policy.
func TestAWSCollectorSDKCallsHaveIAMActions(t *testing.T) {
	root := filepath.Join("..", "providers", "aws", "pkg", "collector")
	used := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		services := map[string]string{}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !strings.Contains(path, "github.com/aws/aws-sdk-go-v2/service/") || strings.HasSuffix(path, "/types") {
				continue
			}
			name := filepath.Base(path)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			switch nameFromSDKPath := filepath.Base(path); nameFromSDKPath {
			case "applicationautoscaling":
				services[name] = "application-autoscaling"
			case "elasticloadbalancingv2":
				services[name] = "elasticloadbalancing"
			case "cloudwatchlogs":
				services[name] = "logs"
			default:
				services[name] = nameFromSDKPath
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			selector, ok := literal.Type.(*ast.SelectorExpr)
			if !ok || !strings.HasSuffix(selector.Sel.Name, "Input") {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok && services[ident.Name] != "" {
				used[services[ident.Name]+":"+strings.TrimSuffix(selector.Sel.Name, "Input")] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	permissions := MustGet("aws").CollectorRolePermissions(Capabilities{Collectors: []CollectorCapability{{
		Type: awsCollectorType, Management: true, Deployment: true,
	}}})
	for action := range used {
		if !containsStr(permissions, action) {
			t.Errorf("Collector AWS SDK call needs undeclared IAM action %s", action)
		}
	}
}

const awsCollectorType = "aws.fargate-collector"

func TestAWSCollectorUninstallGuidanceIsSeparateFromInstall(t *testing.T) {
	s := MustGet("aws")
	for _, mode := range []string{ModeManual, ModeAutomatic} {
		section := "collector-uninstall:" + awsCollectorType + ":" + mode
		guidance := s.SetupArtifactsForSection(section)
		if len(guidance) == 0 {
			t.Errorf("%s uninstall guidance is missing", mode)
		}
		inventoryID := "collector-uninstall-" + mode + "-inventory"
		var inventory string
		for _, artifact := range guidance {
			if artifact.ID == inventoryID {
				inventory = strings.Join(artifact.Steps, " ")
			}
		}
		if inventory == "" {
			t.Errorf("%s uninstall is missing a visible resource inventory", mode)
		}
		required := []string{"task definition", "execution role", "ECS cluster", "VPC", "subnets", "peering"}
		if mode == ModeManual {
			required = append(required, "ECS", "Load Balancer", "target groups", "security group", "credential", "bootstrap", "license", "log group", "scalable target")
		} else {
			required = append(required, "service-linked", "adopted", "retained objects")
		}
		for _, resource := range required {
			if !strings.Contains(strings.ToLower(inventory), strings.ToLower(resource)) {
				t.Errorf("%s uninstall inventory does not name %s", mode, resource)
			}
		}
		for _, artifact := range s.CollectorArtifactsForMode(awsCollectorType, mode) {
			if strings.HasPrefix(artifact.ID, "collector-uninstall-") {
				t.Errorf("%s uninstall step %s appeared in installation", mode, artifact.ID)
			}
		}
	}
}

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
	if len(s.Collectors.Management.Permissions) != 0 {
		t.Errorf("collectors.management should require no account-wide permission for configured regions, got %v", s.Collectors.Management.Permissions)
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
			for _, p := range s.CollectorRolePermissions(caps) {
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
	manual := s.CollectorRolePermissions(Capabilities{Collectors: []CollectorCapability{
		{Type: awsCollectorType, Management: true},
	}})
	auto := s.CollectorRolePermissions(Capabilities{Collectors: []CollectorCapability{
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

// TestAWSCollectorSetupCopyMarksCodeWithPairedBackticks pins the inline-code convention of the Collector
// setup guide. The UI renders `backticked` spans in a description or step as code chips, so an unpaired
// backtick means a span was left open and would print as a stray character.
func TestAWSCollectorSetupCopyMarksCodeWithPairedBackticks(t *testing.T) {
	s := MustGet("aws")
	for _, artifact := range s.SetupArtifacts() {
		if !strings.HasPrefix(artifact.Section, CollectorSection(awsCollectorType)+":") {
			continue
		}
		for _, text := range append([]string{artifact.Description}, artifact.Steps...) {
			if strings.Count(text, "`")%2 != 0 {
				t.Errorf("%s has an unpaired backtick in %q", artifact.ID, text)
			}
		}
	}
}

func TestAWSRegionalCollectorDocumentsAreSpecAuthored(t *testing.T) {
	instructions := MustGet("aws").Collectors.Instructions
	if len(instructions.Notes) == 0 {
		t.Fatal("regional Collector guidance is missing from aws.yaml")
	}
	if len(instructions.Documents) != 3 {
		t.Fatalf("regional Collector documents = %d, want CloudFormation, AWS CLI and Terraform", len(instructions.Documents))
	}
	variants := map[string]bool{}
	for _, document := range instructions.Documents {
		variants[document.Variant] = true
		if document.Title == "" || len(document.Steps) == 0 || document.Format == "" {
			t.Errorf("%s has incomplete document metadata", document.Variant)
		}
		for _, required := range []string{".Region", ".ConnectorName", "{{if .ScalingAutomatic}}"} {
			if !strings.Contains(document.Template, required) {
				t.Errorf("%s template does not render %s from the spec", document.Variant, required)
			}
		}
	}
	for _, variant := range []string{"cloudformation", "aws-cli", "terraform"} {
		if !variants[variant] {
			t.Errorf("regional Collector document %q is missing", variant)
		}
	}
}

func TestAWSCollectorSetupStepsAreSplitByMode(t *testing.T) {
	s := MustGet("aws")
	for _, tc := range []struct {
		mode string
		ids  []string
	}{
		{ModeAutomatic, []string{
			"collector-auto-execution-role",
			"collector-auto-region", "collector-auto-cluster", "collector-auto-network", "collector-auto-egress",
			"collector-auto-size", "collector-auto-scaling-linked-role", "collector-auto-install-values",
		}},
		{ModeManual, []string{
			"collector-manual-execution-role",
			"collector-manual-region", "collector-manual-cluster", "collector-manual-network", "collector-manual-subnets",
			"collector-manual-egress", "collector-manual-size", "collector-manual-security-group", "collector-manual-log-group", "collector-manual-security-group-id",
			"collector-manual-declare", "collector-manual-credential-secret", "collector-manual-issue-credential",
			"collector-manual-store-credential", "collector-manual-license-secret", "collector-manual-apply",
			"collector-manual-verify",
		}},
	} {
		steps := s.CollectorArtifactsForMode(awsCollectorType, tc.mode)
		if len(steps) != len(tc.ids) {
			t.Fatalf("%s has %d steps, want %d", tc.mode, len(steps), len(tc.ids))
		}
		for i, step := range steps {
			if step.ID != tc.ids[i] || step.Variant != "" {
				t.Errorf("%s step %d = %q variant %q, want %q without a variant", tc.mode, i, step.ID, step.Variant, tc.ids[i])
			}
		}
	}
	manual := s.CollectorArtifactsForMode(awsCollectorType, ModeManual)
	if manual[0].Template != s.CollectorArtifactsForMode(awsCollectorType, ModeAutomatic)[0].Template {
		t.Fatal("manual and automatic task execution roles drifted")
	}
}

func TestAWSCollectorSetupMethodsAreSpecAuthored(t *testing.T) {
	s := MustGet("aws")
	management, _ := s.Collectors.Management.findType(awsCollectorType)
	deployment, _ := s.Collectors.Deployment.findType(awsCollectorType)
	for _, mode := range []string{ModeAutomatic, ModeManual} {
		permissions := append([]string{}, management.Permissions...)
		if mode == ModeAutomatic {
			for _, permission := range deployment.Permissions {
				if permission != "iam:PassRole" {
					permissions = append(permissions, permission)
				}
			}
		}
		for _, method := range []string{"aws-cli", "cloudformation"} {
			section := CollectorModeSection(awsCollectorType, mode) + ":" + method
			artifacts := s.SetupArtifactsForSection(section)
			if len(artifacts) == 0 || artifacts[0].VariantLabel == "" {
				t.Fatalf("%s has no labeled %s Collector prerequisite flow", mode, method)
			}
			if !strings.HasSuffix(artifacts[0].ID, "-execution-role") {
				t.Errorf("%s %s flow should start with the ECS task execution role", mode, method)
			}
			for _, artifact := range artifacts {
				if strings.Contains(artifact.Template, "CONNECTOR_PRINCIPAL_ARN") || strings.Contains(artifact.Template, "ConnectorPrincipalArn") || strings.Contains(artifact.ID, "observation-role") || strings.Contains(artifact.ID, "deployment-role") {
					t.Errorf("%s %s still requires a second Collector role", mode, artifact.ID)
				}
			}
			for _, artifact := range artifacts {
				if mode == ModeAutomatic && (strings.Contains(artifact.ID, "declare") || strings.Contains(artifact.ID, "credential") || strings.Contains(artifact.ID, "deploy-service")) {
					t.Errorf("automatic %s flow includes deployment step %s", method, artifact.ID)
				}
				if artifact.Format != "shell" {
					continue
				}
				rendered := renderSetupArtifact(artifact, permissions, map[string]string{"region": "us-east-1", "networkId": "vpc-123", "connectorName": "connector"})
				cmd := exec.Command("bash", "-n")
				cmd.Stdin = strings.NewReader(rendered)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("%s has invalid shell syntax: %v\n%s", artifact.ID, err, output)
				}
			}
		}
	}
}

func TestAWSCollectorPlacementGuideUsesSelectedValuesInBothModes(t *testing.T) {
	s := MustGet("aws")
	for _, mode := range []string{ModeManual, ModeAutomatic} {
		prefix := "collector-manual-"
		if mode == ModeAutomatic {
			prefix = "collector-auto-"
		}
		artifacts := s.CollectorArtifactsForMode(awsCollectorType, mode)
		byID := make(map[string]SetupArtifact, len(artifacts))
		for _, artifact := range artifacts {
			byID[artifact.ID] = artifact
		}
		role := byID[prefix+"execution-role"]
		if !strings.Contains(role.Description, "once") || !strings.Contains(role.Description, "region") {
			t.Errorf("%s role does not explain account-wide reuse", mode)
		}
		cluster := byID[prefix+"cluster"]
		if !strings.Contains(cluster.Description, "ACTIVE") || !strings.Contains(cluster.Template, "{{clusterRef}}") || !strings.Contains(cluster.Template, "list-clusters") {
			t.Errorf("%s cluster step must explain selection and use the entered ARN", mode)
		}
		network := byID[prefix+"network"]
		wants := []string{"describe-vpcs"}
		if mode == ModeAutomatic {
			wants = []string{"describe-vpcs", "describe-subnets", "describe-route-tables", "{{networkId}}", "Subnet IDs are optional", "VPC ID is required"}
		}
		for _, want := range wants {
			if !strings.Contains(network.Template+network.Description, want) {
				t.Errorf("%s network step omits %q", mode, want)
			}
		}
		if mode == ModeManual {
			subnets := byID["collector-manual-subnets"]
			for _, want := range []string{"describe-subnets", "describe-route-tables", "{{networkId}}", "exact subnet IDs"} {
				if !strings.Contains(subnets.Template+subnets.Description, want) {
					t.Errorf("manual subnets step omits %q", want)
				}
			}
		}
		egress := byID[prefix+"egress"]
		for _, want := range []string{"Private", "Public", "public IP", "internal"} {
			if !strings.Contains(egress.Description, want) {
				t.Errorf("%s outbound path step omits %q", mode, want)
			}
		}
		for _, artifact := range []SetupArtifact{cluster, network} {
			rendered := renderSetupArtifact(artifact, nil, map[string]string{
				"region": "us-east-1", "clusterRef": "arn:aws:ecs:us-east-1:123456789012:cluster/example", "networkId": "vpc-123",
			})
			if strings.Contains(rendered, "{{") || strings.Contains(rendered, "<CLUSTER_") || strings.Contains(rendered, "<VPC_") {
				t.Errorf("%s rendered discovery command has an unresolved form value: %s", artifact.ID, rendered)
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(rendered)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s has invalid shell syntax: %v\n%s", artifact.ID, err, output)
			}
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
	manual := s.CollectorRolePermissions(Capabilities{Collectors: []CollectorCapability{
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
	auto := s.CollectorRolePermissions(Capabilities{Collectors: []CollectorCapability{
		{Type: awsCollectorType, Management: true, Deployment: true},
	}})
	if containsStr(auto, "secretsmanager:GetSecretValue") {
		t.Errorf("the connector requires secretsmanager:GetSecretValue; it writes credentials and never reads them back: %v", auto)
	}
}

func TestAWSCollectorModePermissionsBelongToConnectorIdentity(t *testing.T) {
	s := MustGet("aws")
	manual := s.RequiredPermissions(Capabilities{Collectors: []CollectorCapability{{Type: awsCollectorType, Management: true}}})
	auto := s.RequiredPermissions(Capabilities{Collectors: []CollectorCapability{{Type: awsCollectorType, Management: true, Deployment: true}}})
	off := s.RequiredPermissions(Capabilities{})
	for _, action := range []string{"ecs:ListClusters", "ecs:DescribeServices", "ec2:DescribeRegions", "ec2:DescribeVpcs", "elasticloadbalancing:DescribeTargetHealth"} {
		if !containsStr(manual, action) || !containsStr(auto, action) || containsStr(off, action) {
			t.Errorf("%s must be present in manual/automatic and absent when off", action)
		}
	}
	for _, action := range []string{"ecs:CreateService", "secretsmanager:CreateSecret", "iam:PassRole"} {
		if containsStr(manual, action) || !containsStr(auto, action) || containsStr(off, action) {
			t.Errorf("%s must be present only in automatic mode", action)
		}
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
