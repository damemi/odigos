package awsprovider

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"

	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
)

func TestManualCollectorAWSCLIDocumentIsDirectAndRunnableShell(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scaling collectorsettings.ResolvedScaling
		auto    bool
	}{
		{"fixed", collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}, false},
		{"autoscaling", collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 5, TargetCPUPercent: 60, TargetMemoryPercent: 75}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := awsCollectorDocumentForVariant("aws-cli", "us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, tc.scaling)
			if len(doc.Unresolved) != 6 {
				t.Errorf("AWS CLI flow reports %d required inputs, want 6", len(doc.Unresolved))
			}
			for _, required := range []string{"register-task-definition", "create-load-balancer", "create-target-group", "create-listener", "create-service", "ODIGOS_ONPREM_TOKEN", "odigos-iac", "13133"} {
				if !strings.Contains(doc.Content, required) {
					t.Errorf("AWS CLI Collector flow is missing %q", required)
				}
			}
			if strings.Contains(doc.Content, "register-scalable-target") != tc.auto || strings.Contains(doc.Content, "put-scaling-policy") != tc.auto {
				t.Error("AWS CLI Collector flow did not follow the configured scaling mode")
			}
			if strings.Contains(doc.Content, "cloudformation deploy") || strings.Contains(doc.Content, "odigos-managed") {
				t.Error("AWS CLI Collector flow invokes CloudFormation or claims Odigos ownership")
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(doc.Content)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("rendered AWS CLI script has invalid shell syntax: %v\n%s", err, output)
			}
		})
	}
}

func TestManualCollectorAWSCLIDocumentRejectsUnfilledInputsBeforeAWSWrites(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	doc := awsCollectorDocumentForVariant("aws-cli", "us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s)
	dir := t.TempDir()
	mock := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> aws-calls\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", doc.Content)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "A required Collector value or secret is missing") {
		t.Fatalf("unfilled inputs did not fail before deployment: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, "aws-calls")); !os.IsNotExist(err) {
		t.Fatalf("AWS was called before input validation: %v", err)
	}
}

func TestManualCollectorAWSCLIDocumentFailureLeavesCallerShellOpen(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	doc := awsCollectorDocumentForVariant("aws-cli", "us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s)
	cmd := exec.Command("bash", "-c", "printf 'before\\n'\n"+doc.Content+"\nprintf 'after\\n'")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "before\n") || !strings.Contains(string(output), "after\n") || !strings.Contains(string(output), "A required Collector value or secret is missing") {
		t.Fatalf("deployment failure closed the caller shell: %v\n%s", err, output)
	}
}

func TestManualCollectorAWSCLIDocumentReusesExistingDeployment(t *testing.T) {
	scaling := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	values := collectorInstructionValues("us-east-2", "vpc-0123456789abcdef0", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, scaling, false, "sg-0123456789abcdef0")
	values["ClusterRef"] = "arn:aws:ecs:us-east-2:123456789012:cluster/collector-cluster"
	values["ClusterName"] = "collector-cluster"
	values["ExecutionRoleARN"] = "arn:aws:iam::123456789012:role/collector-execution"
	values["SubnetIDs"] = []string{"subnet-0123456789abcdef0", "subnet-0123456789abcdef1"}
	values["CredentialSecretARN"] = "arn:aws:secretsmanager:us-east-2:123456789012:secret:odigos/collector/connector/us-east-2/credential-AbCdEf"
	var doc baseapi.InstructionDocument
	for _, authored := range spec.MustGet("aws").Collectors.Instructions.Documents {
		if authored.Variant == "aws-cli" {
			var err error
			doc, err = renderCollectorDocument(authored, values)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	dir := t.TempDir()
	mock := `#!/bin/sh
printf '%s\n' "$*" >> aws-calls
case "$1 $2" in
  "secretsmanager describe-secret") echo arn:aws:secretsmanager:us-east-2:123456789012:secret:license-AbCdEf ;;
  "ecs register-task-definition") echo arn:aws:ecs:us-east-2:123456789012:task-definition/collector:2 ;;
  "elbv2 describe-load-balancers")
    case "$*" in *DNSName*) echo collector.internal.example ;; *) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:loadbalancer/net/collector/abc ;; esac ;;
  "elbv2 describe-target-groups")
    case "$*" in *4317*) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/grpc/abc ;; *) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/http/abc ;; esac ;;
  "elbv2 describe-listeners")
    case "$*" in *4317*) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:listener/net/grpc/abc ;; *) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:listener/net/http/abc ;; esac ;;
  "ecs describe-services") echo ACTIVE ;;
  "elbv2 create-"*|"ecs create-service") exit 88 ;;
  *) : ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", doc.Content)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "OTLP gRPC: collector.internal.example:4317") {
		t.Fatalf("existing Collector rerun failed: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "aws-calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "ecs update-service") || !strings.Contains(string(calls), "elbv2 modify-listener") || strings.Contains(string(calls), "create-load-balancer") || strings.Contains(string(calls), "create-target-group") || strings.Contains(string(calls), "create-service") {
		t.Errorf("rerun did not reuse AWS resources: %s", calls)
	}
}

func TestManualCollectorAWSCLIDocumentCreatesResourcesWithCorrectTagShapes(t *testing.T) {
	scaling := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 5, TargetCPUPercent: 60, TargetMemoryPercent: 75}
	values := collectorInstructionValues("us-east-2", "vpc-0123456789abcdef0", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, scaling, false, "sg-0123456789abcdef0")
	values["ClusterRef"] = "arn:aws:ecs:us-east-2:123456789012:cluster/collector-cluster"
	values["ClusterName"] = "collector-cluster"
	values["ExecutionRoleARN"] = "arn:aws:iam::123456789012:role/collector-execution"
	values["SubnetIDs"] = []string{"subnet-0123456789abcdef0", "subnet-0123456789abcdef1"}
	values["CredentialSecretARN"] = "arn:aws:secretsmanager:us-east-2:123456789012:secret:odigos/collector/connector/us-east-2/credential-AbCdEf"
	var doc baseapi.InstructionDocument
	for _, authored := range spec.MustGet("aws").Collectors.Instructions.Documents {
		if authored.Variant == "aws-cli" {
			var err error
			doc, err = renderCollectorDocument(authored, values)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	dir := t.TempDir()
	mock := `#!/bin/sh
printf '%s\n' "$*" >> aws-calls
case "$1 $2" in
  "ecs register-task-definition"|"ecs create-service")
    case " $* " in *" --tags key=odigos-component,value=collector "*) : ;; *) echo 'Invalid ECS tags' >&2; exit 88 ;; esac ;;
  "elbv2 create-load-balancer"|"elbv2 create-target-group")
    case " $* " in *" --tags Key=odigos-component,Value=collector "*) : ;; *) echo 'Invalid ELB tags' >&2; exit 88 ;; esac ;;
  "application-autoscaling register-scalable-target")
    case " $* " in *" --tags odigos-component=collector "*) : ;; *) echo 'Invalid scaling tags' >&2; exit 88 ;; esac ;;
esac
case "$1 $2" in
  "secretsmanager describe-secret") echo arn:aws:secretsmanager:us-east-2:123456789012:secret:license-AbCdEf ;;
  "ecs register-task-definition") echo arn:aws:ecs:us-east-2:123456789012:task-definition/collector:1 ;;
  "elbv2 describe-load-balancers")
    case "$*" in *DNSName*) echo collector.internal.example ;; *) echo None ;; esac ;;
  "elbv2 create-load-balancer") echo arn:aws:elasticloadbalancing:us-east-2:123456789012:loadbalancer/net/collector/abc ;;
  "elbv2 describe-target-groups"|"elbv2 describe-listeners"|"ecs describe-services") echo None ;;
  "elbv2 create-target-group")
    case "$*" in *"--port 4317"*) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/grpc/abc ;; *) echo arn:aws:elasticloadbalancing:us-east-2:123456789012:targetgroup/http/abc ;; esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", doc.Content)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "OTLP gRPC: collector.internal.example:4317") {
		t.Fatalf("fresh Collector install failed: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "aws-calls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"ecs register-task-definition", "elbv2 create-load-balancer", "elbv2 create-target-group", "ecs create-service", "application-autoscaling register-scalable-target"} {
		if !strings.Contains(string(calls), operation) {
			t.Errorf("fresh deployment skipped %s", operation)
		}
	}
}

func TestManualCollectorAWSCLIDocumentAWSFailureReportsAndKeepsTerminal(t *testing.T) {
	scaling := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	values := collectorInstructionValues("us-east-2", "vpc-0123456789abcdef0", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, scaling, false, "sg-0123456789abcdef0")
	values["ClusterRef"] = "arn:aws:ecs:us-east-2:123456789012:cluster/collector-cluster"
	values["ClusterName"] = "collector-cluster"
	values["ExecutionRoleARN"] = "arn:aws:iam::123456789012:role/collector-execution"
	values["SubnetIDs"] = []string{"subnet-0123456789abcdef0", "subnet-0123456789abcdef1"}
	values["CredentialSecretARN"] = "arn:aws:secretsmanager:us-east-2:123456789012:secret:odigos/collector/connector/us-east-2/credential-AbCdEf"
	var doc baseapi.InstructionDocument
	for _, authored := range spec.MustGet("aws").Collectors.Instructions.Documents {
		if authored.Variant == "aws-cli" {
			var err error
			doc, err = renderCollectorDocument(authored, values)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte("#!/bin/sh\necho 'Mock AWS failure' >&2\nexit 42\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			cmd := exec.Command(shell, "-c", "printf 'before\\n'\n"+doc.Content+"\nprintf 'after\\n'")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "Mock AWS failure") || !strings.Contains(string(output), "Collector deployment stopped") || !strings.Contains(string(output), "after\n") || strings.Contains(string(output), "read-only variable") {
				t.Fatalf("AWS failure was not reported without closing the caller shell: %v\n%s", err, output)
			}
		})
	}
}

func TestManualCollectorDocumentsIncludeSavedPlacementAndCredential(t *testing.T) {
	scaling := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	values := collectorInstructionValues("us-east-2", "vpc-0123456789abcdef0", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, scaling, false, "sg-0123456789abcdef0")
	values["ClusterRef"] = "arn:aws:ecs:us-east-2:123456789012:cluster/collector-cluster"
	values["ClusterName"] = "collector-cluster"
	values["ExecutionRoleARN"] = "arn:aws:iam::123456789012:role/collector-execution"
	values["SubnetIDs"] = []string{"subnet-0123456789abcdef0", "subnet-0123456789abcdef1"}
	values["SubnetIDsCSV"] = "subnet-0123456789abcdef0,subnet-0123456789abcdef1"
	values["CredentialSecretARN"] = "arn:aws:secretsmanager:us-east-2:123456789012:secret:odigos/collector/connector/us-east-2/credential-AbCdEf"
	for _, authored := range spec.MustGet("aws").Collectors.Instructions.Documents {
		doc, err := renderCollectorDocument(authored, values)
		if err != nil {
			t.Fatalf("render %s: %v", authored.Variant, err)
		}
		if len(doc.Unresolved) != 0 {
			t.Errorf("%s still asks for %v", authored.Variant, doc.Unresolved)
		}
		for _, expected := range []string{"collector-cluster", "collector-execution", "subnet-0123456789abcdef0", "subnet-0123456789abcdef1", "sg-0123456789abcdef0", "credential-AbCdEf"} {
			if !strings.Contains(doc.Content, expected) {
				t.Errorf("%s is missing saved value %s", authored.Variant, expected)
			}
		}
		if !strings.Contains(doc.Content+strings.Join(doc.Steps, "\n"), "odigos/collector/connector/us-east-2/license") {
			t.Errorf("%s does not resolve the licence secret by its known name", authored.Variant)
		}
	}
}

func TestManualCollectorArtifactsContainDataPlaneAndCustomerOwnership(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 3}
	for _, document := range []string{
		awsCloudFormationDocument("eu-west-1", "vpc-123", "connector", "uid", "https://opamp.example", "registry.example/collector@sha256:abc", "512", "1024", "ARM64", 3, s).Content,
		awsTerraformDocument("eu-west-1", "vpc-123", "connector", "uid", "https://opamp.example", "registry.example/collector@sha256:abc", "512", "1024", "ARM64", 3, s).Content,
	} {
		for _, want := range []string{"odigos-iac", "odigos-connector-id", "odigos-managed", "4317", "4318", "13133", "network"} {
			if want == "odigos-managed" {
				if strings.Contains(document, want) {
					t.Fatalf("customer artifact contains ownership tag %q", want)
				}
				continue
			}
			if !strings.Contains(document, want) {
				t.Fatalf("artifact missing %q", want)
			}
		}
		if !strings.Contains(document, "3") || !strings.Contains(document, "ARM64") {
			t.Fatalf("artifact did not render desired count or architecture: %s", document)
		}
	}
}

func TestManualCollectorArtifactsRenderAutoscaling(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 7, TargetCPUPercent: 60, TargetMemoryPercent: 75}
	for _, document := range []string{
		awsCloudFormationDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content,
		awsTerraformDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content,
	} {
		if strings.Contains(document, "%!") {
			t.Fatalf("artifact has an unmatched format verb: %s", document)
		}
		for _, want := range []string{"autoscaling", "cpu", "memory", "60", "75"} {
			if !strings.Contains(strings.ToLower(document), want) {
				t.Fatalf("autoscaling artifact missing %q", want)
			}
		}
	}
}

func TestManualCollectorArtifactRuntimeParity(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingAuto, MinCount: 2, MaxCount: 7}
	cf := awsCloudFormationDocument("eu-west-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s, true)
	tf := awsTerraformDocument("eu-west-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s, true)
	for _, doc := range []string{cf.Content, tf.Content} {
		for _, required := range []string{"password::", "13133", "odigos-iac", "ODIGOS_COLLECTOR_BOOTSTRAP_CONFIG", "120", "100", "200"} {
			if !strings.Contains(doc, required) {
				t.Errorf("artifact missing %q", required)
			}
		}
		if strings.Contains(doc, "%!") {
			t.Errorf("artifact has an unmatched format verb")
		}
	}
	for _, required := range []string{"RepositoryCredentials", "AssignPublicIp: ENABLED", "DependsOn: CollectorService"} {
		if !strings.Contains(cf.Content, required) {
			t.Errorf("CloudFormation missing %q", required)
		}
	}
	for _, required := range []string{"repositoryCredentials", "assign_public_ip = true", "depends_on = [aws_ecs_service.collector]", "ignore_changes = [desired_count]"} {
		if !strings.Contains(tf.Content, required) {
			t.Errorf("Terraform missing %q", required)
		}
	}
	if strings.Contains(cf.Content, "ApplicationAutoscalingRoleArn") || strings.Contains(cf.Content, "RoleARN: !Ref") {
		t.Error("CloudFormation still requires an unnecessary auto scaling role")
	}
	if strings.Contains(cf.Content, "DesiredCount: 2") {
		t.Error("CloudFormation auto scaling must leave desired count to the scalable target")
	}
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte(cf.Content), &parsed); err != nil {
		t.Fatalf("CloudFormation document is not valid YAML: %v", err)
	}
}

func TestManualCollectorArtifactsAttachTargetsAndDeclareOnlyRealPlaceholders(t *testing.T) {
	s := collectorsettings.ResolvedScaling{Mode: collectorsettings.ScalingManual, DesiredCount: 2}
	for _, doc := range []struct {
		name      string
		content   string
		attach    string
		preserve  string
		crossZone string
	}{
		{"cloudformation", awsCloudFormationDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content, "LoadBalancers:", "preserve_client_ip.enabled", "load_balancing.cross_zone.enabled"},
		{"terraform", awsTerraformDocument("us-east-1", "vpc-123", "connector", "uid", "https://opamp.example", "image@sha256:abc", "1024", "2048", "X86_64", 2, s).Content, "load_balancer {", "preserve_client_ip = true", "load_balancing_cross_zone_enabled = \"true\""},
	} {
		if !strings.Contains(doc.content, doc.attach) || !strings.Contains(doc.content, "4317") || !strings.Contains(doc.content, "4318") ||
			!strings.Contains(doc.content, doc.preserve) || !strings.Contains(doc.content, doc.crossZone) {
			t.Errorf("%s does not match the automatic deployment's OTLP target groups", doc.name)
		}
		for _, unresolved := range artifactInputs(doc.content) {
			if !strings.Contains(doc.content, unresolved.Placeholder) {
				t.Errorf("%s claims absent placeholder %s", doc.name, unresolved.Placeholder)
			}
		}
	}
}

// These helpers let format tests select a document; production renders every spec document.
func awsCloudFormationDocument(region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch string, desired int, scaling collectorsettings.ResolvedScaling, assignPublic ...bool) baseapi.InstructionDocument {
	return awsCollectorDocumentForVariant("cloudformation", region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch, desired, scaling, assignPublic...)
}

func awsTerraformDocument(region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch string, desired int, scaling collectorsettings.ResolvedScaling, assignPublic ...bool) baseapi.InstructionDocument {
	return awsCollectorDocumentForVariant("terraform", region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch, desired, scaling, assignPublic...)
}

func awsCollectorDocumentForVariant(variant, region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch string, desired int, scaling collectorsettings.ResolvedScaling, assignPublic ...bool) baseapi.InstructionDocument {
	publicIP := len(assignPublic) > 0 && assignPublic[0]
	values := collectorInstructionValues(region, network, connector, connectorID, opampEndpoint, image, cpu, memory, arch, desired, scaling, publicIP, "sg-0123456789abcdef0")
	for _, document := range spec.MustGet("aws").Collectors.Instructions.Documents {
		if document.Variant == variant {
			rendered, err := renderCollectorDocument(document, values)
			if err != nil {
				panic(err)
			}
			return rendered
		}
	}
	panic("AWS Collector document variant missing from aws.yaml: " + variant)
}
