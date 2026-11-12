package spec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAWSCredentialScriptsCreateTheirOwnPolicy(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, id := range []string{"create-role-aws-cli", "create-user-aws-cli", "workload-policy-aws-cli"} {
			for _, wrongAccount := range []bool{false, true} {
				t.Run(shell+"/"+id+map[bool]string{false: "", true: "/wrong-account"}[wrongAccount], func(t *testing.T) {
					dir := t.TempDir()
					mock := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$MOCK_LOG"
if [ "$1 $2" = 'sts get-caller-identity' ]; then echo "$MOCK_ACCOUNT"; exit 0; fi
case "$1 $2" in
 'iam get-role') test -f role-created; exit $? ;;
 'iam create-role') touch role-created ;;
 'iam get-user'|'iam get-policy') exit 1 ;;
 'iam put-role-policy'|'iam create-policy')
  test -s odigos-policy.json || exit 41 ;;
esac
printf '{}\n'
`
					if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0700); err != nil {
						t.Fatal(err)
					}
					var artifact SetupArtifact
					for _, a := range MustGet("aws").SetupArtifacts() {
						if a.ID == id {
							artifact = a
						}
					}
					if artifact.Template == "" {
						t.Fatalf("missing %s", id)
					}
					script := renderSetupArtifact(artifact, []string{"lambda:ListFunctions", "ecs:RegisterTaskDefinition", "iam:PassRole"}, map[string]string{"accountId": "210987654321", "externalId": "customer-specific-id", "roleName": "OdigosConnector-test"})
					cmd := exec.Command(shell, "-c", script)
					cmd.Dir = dir
					account := "210987654321"
					if wrongAccount {
						account = "000000000000"
					}
					cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MOCK_LOG="+filepath.Join(dir, "calls"), "MOCK_ACCOUNT="+account)
					out, err := cmd.CombinedOutput()
					calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
					if wrongAccount {
						if err == nil || strings.Contains(string(calls), "iam ") {
							t.Fatalf("wrong account must stop before mutations: %v %s %s", err, out, calls)
						}
						return
					}
					if err != nil {
						t.Fatalf("%v\n%s", err, out)
					}
					policy, err := os.ReadFile(filepath.Join(dir, "odigos-policy.json"))
					if err != nil || !json.Valid(policy) {
						t.Fatalf("missing or invalid permissions policy: %v %s", err, policy)
					}
					if !strings.Contains(string(policy), "RestrictPassRoleResources") {
						t.Fatal("missing role scope restrictions")
					}
					if id == "create-role-aws-cli" {
						trust, _ := os.ReadFile(filepath.Join(dir, "odigos-trust-policy.json"))
						if !json.Valid(trust) || !strings.Contains(string(trust), "arn:aws:iam::111111111111:role/Central") {
							t.Fatalf("invalid source trust: %s", trust)
						}
						if !strings.Contains(string(calls), "iam put-role-policy") {
							t.Fatalf("policy never attached: %s", calls)
						}
					} else if id == "workload-policy-aws-cli" {
						if !strings.Contains(string(calls), "iam put-role-policy") || strings.Contains(string(calls), "create-access-key") {
							t.Fatalf("workload identity must attach role policy without issuing keys: %s", calls)
						}
					} else if strings.Contains(string(calls), "put-user-policy") || !strings.Contains(string(calls), "iam attach-user-policy") {
						t.Fatalf("IAM user must use managed policy: %s", calls)
					}
				})
			}
		}
	}
}

func TestAWSOnboardingPolicyAllowsScopedAgentCorePassRole(t *testing.T) {
	for _, artifact := range MustGet("aws").SetupArtifacts() {
		if artifact.ID != "policy-preview" {
			continue
		}
		role := "arn:aws:iam::210987654321:role/AgentCoreExecution"
		raw := renderSetupArtifact(artifact, []string{"bedrock-agentcore:UpdateAgentRuntime", "iam:PassRole"}, map[string]string{"passRoleResources": `["` + role + `"]`})
		var policy struct {
			Statement []struct {
				Sid         string
				NotResource []string
				Condition   map[string]map[string][]string
			}
		}
		// Decode the fields under test individually; unrelated IAM statements have scalar conditions.
		var doc struct{ Statement []json.RawMessage }
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		for _, statement := range doc.Statement {
			var id struct{ Sid string }
			_ = json.Unmarshal(statement, &id)
			if id.Sid != "RestrictPassRoleService" && id.Sid != "RestrictPassRoleResources" {
				continue
			}
			var parsed struct {
				Sid         string
				NotResource []string
				Condition   map[string]map[string][]string
			}
			if err := json.Unmarshal(statement, &parsed); err != nil {
				t.Fatal(err)
			}
			policy.Statement = append(policy.Statement, parsed)
		}
		if len(policy.Statement) != 2 {
			t.Fatal("missing PassRole boundaries")
		}
		services := policy.Statement[0].Condition["StringNotEquals"]["iam:PassedToService"]
		if len(services) != 2 || services[0] != "ecs-tasks.amazonaws.com" || services[1] != "bedrock-agentcore.amazonaws.com" {
			t.Fatalf("AgentCore is denied or service scope is too broad: %v", services)
		}
		if len(policy.Statement[1].NotResource) != 1 || policy.Statement[1].NotResource[0] != role {
			t.Fatal("execution role scope was lost")
		}
	}
}

func TestAWSLambdaScriptsPreserveCustomerLayersInBashAndZsh(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required to exercise the published instructions")
	}
	for _, shell := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, artifact := range MustGet("aws").InstrumentationArtifacts("aws.lambda") {
			if !strings.Contains(artifact.ID, "aws-cli") {
				continue
			}
			t.Run(shell+"/"+artifact.ID, func(t *testing.T) {
				dir := t.TempDir()
				mock := `#!/bin/sh
set -eu
case "$1 $2" in
 'lambda get-function-configuration') printf '%s\n' '{"FunctionArn":"arn:aws:lambda:us-east-1:123456789012:function:example","RevisionId":"rev1","Layers":[{"Arn":"customer-layer"},{"Arn":"arn:aws:lambda:us-east-1:123456789012:layer:odigos-otel-java:1"}],"Environment":{"Variables":{"CUSTOM":"keep"}}}' ;;
 'lambda update-function-configuration') test -s odigos-update.json ;;
 'lambda tag-resource') test -s odigos-tags.json ;;
 'lambda wait') test -s odigos-update.json ;;
 *) exit 42 ;;
esac
`
				if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0700); err != nil {
					t.Fatal(err)
				}
				replacements := strings.NewReplacer("<LAMBDA_CONFIG_QUOTED>", `"{\"v\":2,\"service\":\"example\"}"`, "<LAMBDA_LAYER_ARNS_JSON>", `"sdk-layer", "collector-layer"`, "<LAMBDA_TAGS_JSON>", `{"odigos:iac":"v1"}`, "<FUNCTION_NAME>", "example", "<REGION>", "us-east-1", "<JAVA_LAMBDA_EXEC_WRAPPER>", "/opt/odigos-wrapper", "<LAMBDA_EXEC_WRAPPER>", "/opt/odigos-wrapper", "<WRAPPER>", "/opt/odigos-wrapper")
				cmd := exec.Command(shell, "-c", replacements.Replace(artifact.Template))
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				raw, err := os.ReadFile(filepath.Join(dir, "odigos-update.json"))
				if err != nil {
					t.Fatal(err)
				}
				var update struct {
					Layers      []string
					RevisionId  string
					Environment struct{ Variables map[string]string }
				}
				if err = json.Unmarshal(raw, &update); err != nil {
					t.Fatal(err)
				}
				if strings.Join(update.Layers, ",") != "collector-layer,customer-layer,sdk-layer" || update.RevisionId != "rev1" || update.Environment.Variables["CUSTOM"] != "keep" || update.Environment.Variables["AWS_LAMBDA_EXEC_WRAPPER"] != map[string]string{"python": "/opt/otel-instrument", "java": "/opt/odigos-wrapper", "nodejs": "/opt/odigos-wrapper", "ruby": "/opt/odigos-wrapper"}[artifact.Language] {
					t.Fatalf("unsafe update: %s", raw)
				}
			})
		}
	}
}

func TestAWSSourceSetupScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, id := range []string{"source-user-aws-cli", "source-irsa-aws-cli", "source-existing-aws-cli"} {
			for _, wrongAccount := range []bool{false, true} {
				t.Run(shell+"/"+id+map[bool]string{true: "/wrong-account", false: ""}[wrongAccount], func(t *testing.T) {
					dir := t.TempDir()
					mock := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$MOCK_LOG"
if [ "$1 $2" = 'sts get-caller-identity' ]; then echo "$MOCK_ACCOUNT"; exit 0; fi
case "$1 $2" in
 'iam get-user'|'iam get-role')
  case "$*" in *--query*) echo 'arn:aws:iam::111111111111:role/OdigosSource'; exit 0 ;; *) exit 1 ;; esac ;;
 'iam get-open-id-connect-provider') exit 1 ;;
 'iam put-user-policy'|'iam put-role-policy') test -s odigos-source-assume-policy.json || exit 41 ;;
esac
printf '{}\n'
`
					if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0700); err != nil {
						t.Fatal(err)
					}
					var artifact SetupArtifact
					for _, a := range MustGet("aws").SetupArtifacts() {
						if a.ID == id {
							artifact = a
						}
					}
					if artifact.ID == "" {
						t.Fatal("missing source artifact")
					}
					policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"arn:aws:iam::210987654321:role/Connector","Condition":{"StringEquals":{"sts:ExternalId":"customer-id"}}}]}`
					vars := map[string]string{"sourceName": "OdigosSource", "sourceAccountId": "111111111111", "sourcePolicyName": "OdigosAssume-connector", "sourcePolicyJson": policy, "sourcePolicyIdentityProperty": "role", "createOidcProvider": "true", "oidcIssuer": "oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"}
					script := renderSetupArtifact(artifact, nil, vars)
					cmd := exec.Command(shell, "-c", script)
					cmd.Dir = dir
					account := "111111111111"
					if wrongAccount {
						account = "999999999999"
					}
					cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MOCK_LOG="+filepath.Join(dir, "calls"), "MOCK_ACCOUNT="+account)
					out, err := cmd.CombinedOutput()
					calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
					if wrongAccount {
						if err == nil || strings.Contains(string(calls), "iam ") {
							t.Fatalf("wrong account must stop before IAM calls: %v %s", err, calls)
						}
						return
					}
					if err != nil {
						t.Fatalf("%v\n%s", err, out)
					}
					written, _ := os.ReadFile(filepath.Join(dir, "odigos-source-assume-policy.json"))
					if !json.Valid(written) || string(written) != policy+"\n" {
						t.Fatalf("source policy not preserved: %s", written)
					}
					if id == "source-user-aws-cli" {
						if !strings.Contains(string(calls), "iam create-user") || !strings.Contains(string(calls), "iam create-access-key") {
							t.Fatalf("source credentials not created: %s", calls)
						}
					} else if strings.Contains(string(calls), "create-access-key") {
						t.Fatal("role sources must not issue access keys")
					}
					if id == "source-irsa-aws-cli" {
						trust, _ := os.ReadFile(filepath.Join(dir, "odigos-source-trust.json"))
						if !json.Valid(trust) || !strings.Contains(string(calls), "iam create-open-id-connect-provider") || !strings.Contains(string(calls), "iam create-role") {
							t.Fatalf("IRSA setup incomplete: %s %s", trust, calls)
						}
					}
					if id == "source-existing-aws-cli" && (strings.Contains(string(calls), "create-role") || strings.Contains(string(calls), "create-user")) {
						t.Fatal("existing identity must not be replaced")
					}
				})
			}
		}
	}
}
