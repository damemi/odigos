package spec

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The setup artifacts are copy-paste instructions: a customer pastes the rendered snippet into their
// cloud account. Nothing else in the pipeline parses them — the spec stores them as opaque strings, the
// backend passes them through, and ui-kit does string substitution — so a template that renders into
// invalid JSON/YAML, or that still carries an unsubstituted {{token}}, reaches the customer intact.
// These tests render every artifact the way ui-kit does and check the result.

var setupTokenRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// renderSetupArtifact mirrors the ui-kit renderer (lib/containers.js). Keep the two in lockstep:
//
//  1. Find the single line containing "{{permissions}}". Everything before the token on that line is
//     the indent, re-applied to every rendered item.
//  2. Wrap each permission in ItemTemplate (replacing "{{permission}}"), join with ItemSeparator, and
//     replace the placeholder line. An empty permission list deletes the line entirely.
//  3. Replace every remaining {{name}} token from vars; a name absent from vars is left verbatim.
//
// ItemSeparator is used exactly as declared, with no "\n" fallback: GraphQL types it as String!, so an
// omitted YAML value arrives as "" and joins every permission onto one line. That is a real failure
// mode, not a hypothetical, so the helper reproduces it rather than papering over it.
func renderSetupArtifact(a SetupArtifact, permissions []string, vars map[string]string) string {
	out := a.Template

	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "{{permissions}}") {
			continue
		}
		if len(permissions) == 0 {
			lines = append(lines[:i], lines[i+1:]...)
			break
		}
		indent := line[:strings.Index(line, "{{permissions}}")]
		item := a.ItemTemplate
		if item == "" {
			item = "{{permission}}"
		}
		rendered := make([]string, len(permissions))
		for j, p := range permissions {
			rendered[j] = indent + strings.ReplaceAll(item, "{{permission}}", p)
		}
		lines[i] = strings.Join(rendered, a.ItemSeparator)
		break
	}
	out = strings.Join(lines, "\n")

	return setupTokenRe.ReplaceAllStringFunc(out, func(tok string) string {
		if v, ok := vars[setupTokenRe.FindStringSubmatch(tok)[1]]; ok {
			return v
		}
		return tok
	})
}

// TestSetupArtifactsRender renders every provider's artifacts with a representative permission set and
// a fully-populated var map, then checks the output is syntactically valid for its declared format and
// carries no leftover tokens.
func TestSetupArtifactsRender(t *testing.T) {
	permissions := []string{"sts:GetCallerIdentity", "ec2:DescribeRegions", "lambda:ListFunctions"}

	for _, provider := range Providers() {
		s := MustGet(provider)
		for _, a := range s.SetupArtifacts() {
			t.Run(provider+"/"+a.ID, func(t *testing.T) {
				if a.Template == "" {
					return // metadata-only artifact (e.g. the credentials panel)
				}

				// A template that embeds the permission list needs both rendering knobs: without an
				// itemTemplate the item is a bare string (invalid in a JSON array and a YAML list), and
				// without an itemSeparator every permission lands on one line.
				if strings.Contains(a.Template, "{{permissions}}") {
					if a.ItemTemplate == "" {
						t.Errorf("embeds {{permissions}} but declares no itemTemplate")
					}
					if a.ItemSeparator == "" {
						t.Errorf("embeds {{permissions}} but declares no itemSeparator; all permissions would render on one line")
					}
				}

				// Fill every scalar token the template declares, so a leftover token below means the
				// renderer could not resolve it rather than that the test under-supplied vars.
				vars := map[string]string{}
				for _, m := range setupTokenRe.FindAllStringSubmatch(a.Template, -1) {
					if m[1] != "permissions" {
						vars[m[1]] = "test-" + m[1]
					}
				}

				got := renderSetupArtifact(a, permissions, vars)

				if tok := setupTokenRe.FindString(got); tok != "" {
					t.Errorf("rendered output still contains %s:\n%s", tok, got)
				}

				switch a.Format {
				case "json":
					var v any
					if err := json.Unmarshal([]byte(got), &v); err != nil {
						t.Errorf("format: json but rendered output does not parse: %v\n%s", err, got)
					}
				case "yaml":
					var v any
					if err := yaml.Unmarshal([]byte(got), &v); err != nil {
						t.Errorf("format: yaml but rendered output does not parse: %v\n%s", err, got)
					}
				case "shell", "text":
					// No parser; the token and permission-rendering checks above are the coverage.
				default:
					t.Errorf("unknown format %q (want json|yaml|shell|text)", a.Format)
				}

				for _, p := range permissions {
					if strings.Contains(a.Template, "{{permissions}}") && !strings.Contains(got, p) {
						t.Errorf("rendered output dropped permission %q:\n%s", p, got)
					}
				}
			})
		}
	}
}

// TestAWSCloudFormationTemplateShape renders the CloudFormation artifact and walks the parsed result,
// because "parses as YAML" is a weak claim for a document whose value is entirely in its structure: the
// role, the trust condition, the permission list and the RoleArn output are what the steps promise.
func TestAWSCloudFormationTemplateShape(t *testing.T) {
	s := MustGet("aws")
	var art SetupArtifact
	for _, a := range s.SetupArtifactsForSection("create-role") {
		if a.Variant == "cloudformation" {
			art = a
		}
	}
	if art.ID == "" {
		t.Fatal("no cloudformation create-role artifact declared")
	}

	permissions := []string{"sts:GetCallerIdentity", "ecs:RegisterTaskDefinition"}
	rendered := renderSetupArtifact(art, permissions, map[string]string{"externalId": "odigos-ext-id"})

	var tmpl struct {
		Parameters map[string]struct {
			Type      string `yaml:"Type"`
			MinLength int    `yaml:"MinLength"`
			Default   string `yaml:"Default"`
		} `yaml:"Parameters"`
		Resources map[string]struct {
			Type       string `yaml:"Type"`
			Properties struct {
				RoleName                 string `yaml:"RoleName"`
				AssumeRolePolicyDocument struct {
					Statement []struct {
						Effect    string         `yaml:"Effect"`
						Principal map[string]any `yaml:"Principal"`
						Condition struct {
							StringEquals map[string]any `yaml:"StringEquals"`
						} `yaml:"Condition"`
					} `yaml:"Statement"`
				} `yaml:"AssumeRolePolicyDocument"`
				Policies []struct {
					PolicyName     string `yaml:"PolicyName"`
					PolicyDocument struct {
						Statement []struct {
							Action []string `yaml:"Action"`
						} `yaml:"Statement"`
					} `yaml:"PolicyDocument"`
				} `yaml:"Policies"`
			} `yaml:"Properties"`
		} `yaml:"Resources"`
		Outputs map[string]any `yaml:"Outputs"`
	}
	// !Ref / !GetAtt are CloudFormation short-form tags; yaml.v3 rejects unknown tags on typed decode,
	// so scrub them to plain scalars first. The tags themselves are checked as text below.
	scrubbed := strings.NewReplacer("!Ref ", "", "!GetAtt ", "").Replace(rendered)
	if err := yaml.Unmarshal([]byte(scrubbed), &tmpl); err != nil {
		t.Fatalf("cloudformation template does not parse: %v\n%s", err, rendered)
	}

	// The external id must be a required parameter, not just a substituted literal: a blank external id
	// yields a role no one can assume, and MinLength turns that into a deploy-time failure.
	p, ok := tmpl.Parameters["OdigosExternalId"]
	if !ok {
		t.Fatalf("template declares no OdigosExternalId parameter:\n%s", rendered)
	}
	if p.MinLength < 1 {
		t.Errorf("OdigosExternalId.MinLength = %d, want >= 1 so an empty external id fails the deploy", p.MinLength)
	}
	if p.Default != "odigos-ext-id" {
		t.Errorf("OdigosExternalId.Default = %q, want the substituted form value", p.Default)
	}

	role, ok := tmpl.Resources["OdigosConnectorRole"]
	if !ok {
		t.Fatalf("template declares no OdigosConnectorRole resource:\n%s", rendered)
	}
	if role.Type != "AWS::IAM::Role" {
		t.Errorf("OdigosConnectorRole.Type = %q, want AWS::IAM::Role", role.Type)
	}
	// RoleName is what forces CAPABILITY_NAMED_IAM, which the steps tell the user to acknowledge.
	if role.Properties.RoleName == "" {
		t.Error("role declares no RoleName, but the steps tell the user to acknowledge a named IAM resource")
	}

	trust := role.Properties.AssumeRolePolicyDocument.Statement
	if len(trust) != 1 {
		t.Fatalf("trust policy statements = %d, want 1", len(trust))
	}
	if _, ok := trust[0].Condition.StringEquals["sts:ExternalId"]; !ok {
		t.Errorf("trust policy has no sts:ExternalId condition:\n%s", rendered)
	}
	if _, ok := trust[0].Principal["AWS"]; !ok {
		t.Errorf("trust policy has no AWS principal:\n%s", rendered)
	}

	if len(role.Properties.Policies) != 1 {
		t.Fatalf("inline policies = %d, want 1", len(role.Properties.Policies))
	}
	gotActions := role.Properties.Policies[0].PolicyDocument.Statement[0].Action
	if len(gotActions) != len(permissions) {
		t.Errorf("policy actions = %v, want the %d rendered permissions", gotActions, len(permissions))
	}

	// The steps end with "copy the RoleArn value", so the output has to exist and be the role's ARN.
	if _, ok := tmpl.Outputs["RoleArn"]; !ok {
		t.Errorf("template declares no RoleArn output, but the steps tell the user to copy one:\n%s", rendered)
	}
	if !strings.Contains(rendered, "!GetAtt OdigosConnectorRole.Arn") {
		t.Errorf("RoleArn output should be !GetAtt OdigosConnectorRole.Arn:\n%s", rendered)
	}
}

// TestAWSManualTrustPolicyRenders checks the hand-pasted trust policy, which is the one artifact with no
// tooling between the customer and IAM.
func TestAWSManualTrustPolicyRenders(t *testing.T) {
	s := MustGet("aws")
	var art SetupArtifact
	for _, a := range s.SetupArtifactsForSection("create-role") {
		if a.Variant == "manual" {
			art = a
		}
	}
	if art.ID == "" {
		t.Fatal("no manual create-role artifact declared")
	}

	rendered := renderSetupArtifact(art, nil, map[string]string{"externalId": "odigos-ext-id"})

	var doc struct {
		Version   string `json:"Version"`
		Statement []struct {
			Effect    string            `json:"Effect"`
			Principal map[string]string `json:"Principal"`
			Action    string            `json:"Action"`
			Condition struct {
				StringEquals map[string]string `json:"StringEquals"`
			} `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(rendered), &doc); err != nil {
		t.Fatalf("manual trust policy does not parse as JSON: %v\n%s", err, rendered)
	}
	if len(doc.Statement) != 1 {
		t.Fatalf("trust policy statements = %d, want 1", len(doc.Statement))
	}
	st := doc.Statement[0]
	if st.Action != "sts:AssumeRole" || st.Effect != "Allow" {
		t.Errorf("trust statement = %s %s, want Allow sts:AssumeRole", st.Effect, st.Action)
	}
	if got := st.Condition.StringEquals["sts:ExternalId"]; got != "odigos-ext-id" {
		t.Errorf("sts:ExternalId = %q, want the substituted form value", got)
	}
	if !strings.HasPrefix(st.Principal["AWS"], "arn:aws:iam::") {
		t.Errorf("trust principal = %q, want an arn:aws:iam:: principal", st.Principal["AWS"])
	}
}
