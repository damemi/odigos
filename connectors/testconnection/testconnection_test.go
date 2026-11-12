package testconnection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/odigos-io/odigos/connectors/spec"
)

type apiErr struct{ code string }

func (e *apiErr) Error() string                 { return e.code }
func (e *apiErr) ErrorCode() string             { return e.code }
func (e *apiErr) ErrorMessage() string          { return e.code }
func (e *apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestIsAuthErr(t *testing.T) {
	if !isAuthErr(&apiErr{code: "AccessDeniedException"}) {
		t.Error("AccessDeniedException should be an auth error")
	}
	if !isAuthErr(errors.New("User is not authorized to perform: lambda:ListFunctions")) {
		t.Error("'not authorized' message should be an auth error")
	}
	if isAuthErr(errors.New("dial tcp: connection refused")) {
		t.Error("connectivity error should NOT be an auth error")
	}
}

func TestPermissionsFor(t *testing.T) {
	s := spec.MustGet("aws")
	caps := spec.Capabilities{Workloads: []spec.WorkloadCapability{{Type: "aws.lambda", Discovery: true}}}
	lambdaPerms := permissionsFor(s, caps, "lambda:")
	if !strings.Contains(lambdaPerms, "lambda:ListFunctions") {
		t.Errorf("expected lambda:ListFunctions in remediation, got %q", lambdaPerms)
	}
	if strings.Contains(lambdaPerms, "ecs:") {
		t.Errorf("lambda remediation should not include ecs permissions, got %q", lambdaPerms)
	}
}

// TestBuildConfigAllAuthModes verifies the tester can resolve and wire an aws.Config for every auth
// mode the spec declares — exercising the spec↔tester coupling offline (no AWS calls).
func TestBuildConfigAllAuthModes(t *testing.T) {
	auth := spec.MustGet("aws").Auth
	cases := []struct {
		name  string
		creds map[string]string
	}{
		{"workload-identity", nil},
		{"static-keys", map[string]string{"accessKeyId": "AKIA", "secretAccessKey": "s"}},
		{"assume-role on workload-identity", map[string]string{"roleArn": "arn:aws:iam::1:role/r"}},
		{"assume-role on static-keys", map[string]string{"roleArn": "arn:aws:iam::1:role/r", "accessKeyId": "AKIA", "secretAccessKey": "s"}},
	}
	for _, tc := range cases {
		chain, err := auth.Resolve(tc.creds)
		if err != nil {
			t.Errorf("%s: Resolve: %v", tc.name, err)
			continue
		}
		if _, err := buildConfig(context.Background(), "us-east-1", tc.creds, chain); err != nil {
			t.Errorf("%s: buildConfig: %v", tc.name, err)
		}
	}
}

func TestResultFinalize(t *testing.T) {
	r := &Result{}
	r.add(CheckResult{Passed: true})
	r.add(CheckResult{Passed: false})
	r.finalize()
	if r.Success {
		t.Error("result with a failing check should not be Success")
	}

	r2 := &Result{}
	r2.add(CheckResult{Passed: true})
	r2.finalize()
	if !r2.Success {
		t.Error("result with only passing checks should be Success")
	}

	empty := &Result{}
	empty.finalize()
	if empty.Success {
		t.Error("empty result should not be Success")
	}
}
