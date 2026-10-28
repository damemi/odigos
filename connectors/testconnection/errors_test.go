package testconnection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDiscoveryErrorsNeverPass(t *testing.T) {
	for _, provider := range []struct {
		name    string
		summary func(error) string
		auth    func(error) bool
		errors  []error
	}{
		{"gcp", summarizeGCPErr, isGCPAuthErr, []error{status.Error(codes.Unavailable, "API unavailable"), status.Error(codes.PermissionDenied, "required permission missing"), &googleapi.Error{Code: 429, Message: "quota exceeded"}, context.DeadlineExceeded, errors.New("dial tcp: connection refused")}},
		{"azure", summarizeAzureErr, isAzureAuthErr, []error{&azcore.ResponseError{StatusCode: 503, ErrorCode: "ServiceUnavailable"}, &azcore.ResponseError{StatusCode: 403, ErrorCode: "AuthorizationFailed"}, &azcore.ResponseError{StatusCode: 429, ErrorCode: "TooManyRequests"}, context.DeadlineExceeded, errors.New("dial tcp: connection refused")}},
	} {
		for _, err := range provider.errors {
			t.Run(provider.name+"/"+err.Error(), func(t *testing.T) {
				check := discoveryCheck(CheckResult{DisplayName: "Discovery", Message: "allowed", Remediation: "grant required permission"}, err, provider.summary, provider.auth)
				if check.Passed || !strings.Contains(check.Message, provider.summary(err)) || check.Remediation == "" {
					t.Fatalf("lost provider failure: %+v", check)
				}
				if !provider.auth(err) && strings.HasPrefix(check.Remediation, "grant") {
					t.Fatal("network/service error incorrectly treated as missing permissions")
				}
			})
		}
		if check := discoveryCheck(CheckResult{Message: "allowed", Remediation: "grant permission"}, nil, provider.summary, provider.auth); !check.Passed || check.Message != "allowed" || check.Remediation != "" {
			t.Fatalf("success changed: %+v", check)
		}
	}
}

func TestAzureDiagnosticMessageAndRequestID(t *testing.T) {
	err := &azcore.ResponseError{StatusCode: 403, ErrorCode: "AuthorizationFailed", RawResponse: &http.Response{Header: http.Header{"X-Ms-Request-Id": []string{"azure-request-123"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Caller lacks Microsoft.App/containerApps/read"},"other":"never show raw body"}`))}}
	got := summarizeAzureErr(fmt.Errorf("wrapped: %w", err))
	for _, expected := range []string{"AuthorizationFailed", "Microsoft.App/containerApps/read", "azure-request-123"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %q: %s", expected, got)
		}
	}
	if strings.Contains(got, "never show raw body") {
		t.Fatal("raw response leaked")
	}
}

func TestSafeMessagePreservesDiagnosticsAndRedactsSecrets(t *testing.T) {
	for _, tc := range []struct {
		provider            string
		creds               map[string]string
		message, diagnostic string
	}{
		{"aws", map[string]string{"accessKeyId": "AKIATESTPRIVATE", "secretAccessKey": "secret-value", "sessionToken": "session-value"}, "AccessDenied AKIATESTPRIVATE secret-value session-value", "AccessDenied"},
		{"gcp", map[string]string{"credentialsJson": `{"private_key":"private-key-value","private_key_id":"private-id"}`}, `PermissionDenied private-key-value private-id {\"private_key\":\"private-key-value\",\"private_key_id\":\"private-id\"}`, "PermissionDenied"},
		{"azure", map[string]string{"clientSecret": "secret-value"}, "AADSTS7000215 invalid client secret secret-value", "AADSTS7000215"},
	} {
		got := SafeMessage(tc.provider, tc.creds, tc.message)
		if !strings.Contains(got, tc.diagnostic) || !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("lost safe diagnostics: %s", got)
		}
		for _, secret := range []string{"secret-value", "session-value", "AKIATESTPRIVATE", "private-key-value", "private-id"} {
			if strings.Contains(got, secret) {
				t.Fatalf("secret leaked for %s", tc.provider)
			}
		}
	}
}

type deadlineTester struct{}

func (deadlineTester) Provider() string { return "aws" }
func (deadlineTester) Test(ctx context.Context, in Input) (*Result, error) {
	<-ctx.Done()
	return &Result{Checks: []CheckResult{{Name: "discovery", Passed: true, Message: "allowed"}}}, nil
}

func TestRunDeadlineCannotBecomeSuccessfulVerification(t *testing.T) {
	original := registry["aws"]
	registry["aws"] = deadlineTester{}
	t.Cleanup(func() { registry["aws"] = original })
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	result, err := Run(ctx, Input{Provider: "aws"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || len(result.Checks) != 2 || result.Checks[1].Name != "verification_timeout" {
		t.Fatalf("deadline hidden: %+v", result)
	}
}
