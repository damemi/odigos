package spec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const collectorTestAccount = "123456789012"

func collectorSetupArtifact(t *testing.T, id, mode, method string) SetupArtifact {
	t.Helper()
	section := CollectorModeSection(awsCollectorType, mode) + ":" + method
	for _, artifact := range MustGet("aws").SetupArtifactsForSection(section) {
		if artifact.ID == id {
			return artifact
		}
	}
	t.Fatalf("missing Collector setup artifact %s", id)
	return SetupArtifact{}
}

func collectorRenderedArtifact(t *testing.T, id, mode, method string) string {
	t.Helper()
	s := MustGet("aws")
	management, _ := s.Collectors.Management.findType(awsCollectorType)
	deployment, _ := s.Collectors.Deployment.findType(awsCollectorType)
	permissions := append([]string{}, management.Permissions...)
	if mode == ModeAutomatic {
		for _, action := range deployment.Permissions {
			if action != "iam:PassRole" {
				permissions = append(permissions, action)
			}
		}
	}
	permissions = uniqueSorted(permissions)
	return renderSetupArtifact(collectorSetupArtifact(t, id, mode, method), permissions, map[string]string{
		"connectorName": "example", "region": "us-east-1", "networkId": "vpc-123",
	})
}

// Execute the published snippets with a fake AWS CLI. Parsing the heredoc before shell
// expansion missed zsh's $ACCOUNT_ID:l modifier, which corrupted both IAM resource ARNs.
func TestAWSCollectorExecutionRoleCommandsExpandInBashAndZsh(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, mode := range []string{ModeAutomatic, ModeManual} {
			t.Run(shell+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				mockAWSCLI(t, dir)
				id := "collector-auto-cli-execution-role"
				if mode == ModeManual {
					id = "collector-manual-cli-execution-role"
				}
				cmd := exec.Command(shell, "-c", collectorRenderedArtifact(t, id, mode, "aws-cli"))
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("rendered command failed: %v\n%s", err, output)
				}
				data, err := os.ReadFile(filepath.Join(dir, "odigos-collector-task-policy.json"))
				if err != nil {
					t.Fatal(err)
				}
				var policy struct {
					Statement []struct {
						Resource string `json:"Resource"`
					} `json:"Statement"`
				}
				if err := json.Unmarshal(data, &policy); err != nil {
					t.Fatalf("generated policy is invalid JSON: %v", err)
				}
				want := []string{
					"arn:aws:logs:*:" + collectorTestAccount + ":log-group:/odigos/collector/example:*",
					"arn:aws:secretsmanager:*:" + collectorTestAccount + ":secret:odigos/collector/example/*",
				}
				got := []string{policy.Statement[0].Resource, policy.Statement[1].Resource}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("resource ARNs = %v, want %v", got, want)
				}
				trust, err := os.ReadFile(filepath.Join(dir, "odigos-collector-task-trust.json"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(trust), "ecs-tasks.amazonaws.com") || strings.Contains(string(trust), "CONNECTOR_PRINCIPAL") {
					t.Errorf("task role trust is wrong: %s", trust)
				}
			})
		}
	}
}

func TestAWSCollectorCLIExecutionRoleCanResumeAfterPolicyFailure(t *testing.T) {
	dir := t.TempDir()
	mockAWSCLI(t, dir)
	cmd := exec.Command("bash", "-c", collectorRenderedArtifact(t, "collector-auto-cli-execution-role", ModeAutomatic, "aws-cli"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MOCK_EXISTING_ROLE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rerun failed: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "aws-calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "iam update-assume-role-policy") || !strings.Contains(string(calls), "iam put-role-policy") || strings.Contains(string(calls), "iam create-role") {
		t.Errorf("existing role was not safely completed: %s", calls)
	}
}

func TestAWSCollectorCloudFormationRolePrecedesPlacement(t *testing.T) {
	for _, mode := range []string{ModeAutomatic, ModeManual} {
		id := "collector-auto-cfn-execution-role"
		if mode == ModeManual {
			id = "collector-manual-cfn-execution-role"
		}
		role := collectorSetupArtifact(t, id, mode, "cloudformation")
		steps := strings.Join(role.Steps, "\n")
		if !strings.Contains(steps, "cloudformation deploy") || !strings.Contains(steps, "ExecutionRoleArn") {
			t.Errorf("%s does not create the task execution role before region selection", id)
		}
		if strings.Contains(role.Template, "ConnectorPrincipalArn") || strings.Contains(role.Template, "ObservationRole") || strings.Contains(role.Template, "DeploymentRole") {
			t.Errorf("%s requires an extra connector role", id)
		}
	}

	dir := t.TempDir()
	mockAWSCLI(t, dir)
	cmd := exec.Command("bash", "-c", collectorRenderedArtifact(t, "collector-manual-cfn-apply", ModeManual, "cloudformation"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CloudFormation prerequisites failed: %v\n%s", err, output)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "aws-calls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "-prerequisites") || strings.Contains(string(calls), "-execution") || strings.Contains(string(calls), "-observation") || strings.Contains(string(calls), "-deployment") {
		t.Errorf("prerequisites repeated role deployment or created an extra role: %s", calls)
	}
	if strings.Contains(string(calls), "WorkloadCidr") {
		t.Errorf("prerequisites require workload CIDR before Collector deployment: %s", calls)
	}
}

func TestAWSCollectorInstallNetworkSteps(t *testing.T) {
	for _, mode := range []string{ModeAutomatic, ModeManual} {
		for _, artifact := range MustGet("aws").SetupArtifactsForSection(CollectorModeSection(awsCollectorType, mode)) {
			if strings.Contains(strings.ToLower(artifact.Title), "workload") || strings.Contains(strings.ToLower(strings.Join(artifact.Steps, "\n")), "peering") {
				t.Errorf("%s install flow includes workload routing step %s", mode, artifact.ID)
			}
		}
	}
	for _, id := range []string{"collector-manual-cli-security-logs", "collector-manual-cfn-security-logs", "collector-manual-cfn-apply"} {
		method := "aws-cli"
		if strings.Contains(id, "cfn") {
			method = "cloudformation"
		}
		artifact := collectorSetupArtifact(t, id, ModeManual, method)
		if strings.Contains(artifact.Template, "WORKLOAD_CIDR") || strings.Contains(artifact.Template, "WorkloadCidr") {
			t.Errorf("%s asks for sender CIDR before deployment", id)
		}
	}
	step := MustGet("aws").SetupArtifactsForSection(CollectorModeSection(awsCollectorType, ModeManual))
	ids := make(map[string]int, len(step))
	for index, artifact := range step {
		ids[artifact.ID] = index
	}
	if !(ids["collector-manual-network"] < ids["collector-manual-subnets"] && ids["collector-manual-subnets"] < ids["collector-manual-declare"] && ids["collector-manual-apply"] < ids["collector-manual-verify"] && step[len(step)-1].ID == "collector-manual-verify") {
		t.Errorf("manual network step order is wrong: %v", ids)
	}
}

func TestAWSCollectorSecurityGroupPrerequisiteReusesExistingResources(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "reuse"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/bin/sh
printf '%s\n' "$*" >> aws-calls
case "$*" in
  *"describe-security-groups "*"--filters"*)
    if [ "$MOCK_EXISTING" = 1 ]; then echo sg-0123456789abcdef0; else echo None; fi ;;
  *"describe-security-groups "*"--group-ids"*)
    if [ "$MOCK_EXISTING" = 1 ]; then echo sg-0123456789abcdef0; else echo None; fi ;;
  *"create-security-group"*)
    [ "$MOCK_EXISTING" = 0 ] || exit 88
    echo sg-0123456789abcdef0 ;;
  *"authorize-security-group-ingress"*|*"create-log-group"*)
    [ "$MOCK_EXISTING" = 0 ] || exit 88 ;;
  *"describe-log-groups"*)
    if [ "$MOCK_EXISTING" = 1 ]; then echo /odigos/collector/example; else echo None; fi ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
				t.Fatal(err)
			}
			flag := "0"
			if existing {
				flag = "1"
			}
			cmd := exec.Command("bash", "-c", collectorRenderedArtifact(t, "collector-manual-cli-security-logs", ModeManual, "aws-cli"))
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MOCK_EXISTING="+flag)
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "SecurityGroupId=sg-0123456789abcdef0") {
				t.Fatalf("prerequisite failed: %v\n%s", err, output)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "aws-calls"))
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"create-security-group", "authorize-security-group-ingress", "create-log-group"} {
				if strings.Contains(string(calls), operation) == existing {
					t.Errorf("%s call is wrong for existing=%v: %s", operation, existing, calls)
				}
			}
		})
	}
}

func TestAWSCollectorCredentialSecretStepReusesExistingSecret(t *testing.T) {
	var artifact SetupArtifact
	for _, candidate := range MustGet("aws").SetupArtifactsForSection(CollectorModeSection(awsCollectorType, ModeManual)) {
		if candidate.ID == "collector-manual-credential-secret" {
			artifact = candidate
			break
		}
	}
	if artifact.ID == "" {
		t.Fatal("missing credential secret step")
	}
	command := renderSetupArtifact(artifact, nil, map[string]string{
		"connectorName": "example", "region": "us-east-1",
	})
	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "reuse"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/bin/sh
printf '%s\n' "$*" >> aws-calls
case "$1 $2" in
  "secretsmanager describe-secret")
    if [ "$MOCK_EXISTING" = 1 ]; then
      echo arn:aws:secretsmanager:us-east-1:123456789012:secret:odigos/collector/example/us-east-1/credential-AbCdEf
    else
      echo 'ResourceNotFoundException: secret not found' >&2
      exit 1
    fi ;;
  "secretsmanager create-secret")
    [ "$MOCK_EXISTING" = 0 ] || exit 88
    echo arn:aws:secretsmanager:us-east-1:123456789012:secret:odigos/collector/example/us-east-1/credential-AbCdEf ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
				t.Fatal(err)
			}
			flag := "0"
			if existing {
				flag = "1"
			}
			cmd := exec.Command("bash", "-c", command)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MOCK_EXISTING="+flag)
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "credential-AbCdEf") {
				t.Fatalf("credential secret step failed: %v\n%s", err, output)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "aws-calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "create-secret") == existing {
				t.Errorf("credential secret creation is wrong for existing=%v: %s", existing, calls)
			}
		})
	}
}

func TestAWSCollectorInstructionsDoNotSetErrexit(t *testing.T) {
	for _, artifact := range MustGet("aws").SetupArtifacts() {
		if strings.HasPrefix(artifact.Section, "collector:") && strings.Contains(artifact.Template, "set -e") {
			t.Errorf("%s still sets errexit", artifact.ID)
		}
	}
	for _, document := range MustGet("aws").Collectors.Instructions.Documents {
		if strings.Contains(document.Template, "set -e") {
			t.Errorf("%s still sets errexit", document.ID)
		}
	}
}

func mockAWSCLI(t *testing.T, dir string) {
	t.Helper()
	mock := `#!/bin/sh
printf '%s\n' "$*" >> aws-calls
case "$*" in
  "sts get-caller-identity --query Account --output text") echo 123456789012 ;;
  "iam get-role "*" --query Role.Arn --output text") echo arn:aws:iam::123456789012:role/example ;;
  "iam get-role "*) [ "${MOCK_EXISTING_ROLE:-}" = 1 ] ;;
  "cloudformation describe-stacks "*"ExecutionRoleArn"*) echo arn:aws:iam::123456789012:role/example-execution ;;
  "cloudformation describe-stacks "*"DeploymentRoleArn"*) echo arn:aws:iam::123456789012:role/example-deployment ;;
  "cloudformation describe-stacks "*"ObservationRoleArn"*) echo arn:aws:iam::123456789012:role/example-observation ;;
  *) : ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestAWSCollectorCloudFormationExecutionRoleResources(t *testing.T) {
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(collectorRenderedArtifact(t, "collector-auto-cfn-execution-role", ModeAutomatic, "cloudformation")), &parsed); err != nil {
		t.Fatal(err)
	}
	role := parsed["Resources"].(map[string]any)["CollectorExecutionRole"].(map[string]any)["Properties"].(map[string]any)
	statements := role["Policies"].([]any)[0].(map[string]any)["PolicyDocument"].(map[string]any)["Statement"].([]any)
	want := []string{
		"arn:${AWS::Partition}:logs:*:${AWS::AccountId}:log-group:/odigos/collector/${ConnectorName}:*",
		"arn:${AWS::Partition}:secretsmanager:*:${AWS::AccountId}:secret:odigos/collector/${ConnectorName}/*",
	}
	for i, statement := range statements {
		if got := statement.(map[string]any)["Resource"]; got != want[i] {
			t.Errorf("execution role resource %d = %v, want %s", i, got, want[i])
		}
	}
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
