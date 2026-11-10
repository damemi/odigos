package testconnection

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/spec"
)

func TestAWSAssumeRoleCacheSharedAcrossConcurrentRegions(t *testing.T) {
	var assumes, calls, active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "functions") {
			calls.Add(1)
			current := active.Add(1)
			for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Functions":[]}`)
			return
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/xml")
		switch r.FormValue("Action") {
		case "AssumeRole":
			assumes.Add(1)
			fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIATEST</AccessKeyId><SecretAccessKey>test-secret</SecretAccessKey><SessionToken>test-session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "GetCallerIdentity":
			fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>123456789012</Account><Arn>arn:aws:sts::123456789012:assumed-role/Connector/test</Arn></GetCallerIdentityResult></GetCallerIdentityResponse>`)
		case "DescribeRegions":
			fmt.Fprint(w, `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><regionInfo>`)
			for i := 1; i <= 12; i++ {
				fmt.Fprintf(w, `<item><regionName>us-east-%d</regionName></item>`, i)
			}
			fmt.Fprint(w, `</regionInfo></DescribeRegionsResponse>`)
		default:
			t.Errorf("unexpected action %s", r.FormValue("Action"))
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL", server.URL)
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	result, err := Run(context.Background(), Input{Provider: "aws", Credentials: map[string]string{"accessKeyId": "caller-key", "secretAccessKey": "caller-secret", "roleArn": "arn:aws:iam::123456789012:role/Connector", "externalId": "customer-id"}, Capabilities: spec.Capabilities{Workloads: []spec.WorkloadCapability{{Type: "aws.lambda", Discovery: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || assumes.Load() != 1 || calls.Load() != 12 || peak.Load() < 2 || peak.Load() > 6 {
		t.Fatalf("success=%v assumes=%d calls=%d parallel=%d checks=%+v", result.Success, assumes.Load(), calls.Load(), peak.Load(), result.Checks)
	}
}

// Exercise the real SDK request path without AWS resources. A valid identity and region
// list must not hide failed discovery or resource-scoped read permissions.
func TestAWSDiscoveryFailuresAreReported(t *testing.T) {
	for _, failure := range []string{"list-unavailable", "tags-denied", "none"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "functions") {
					w.Header().Set("Content-Type", "application/json")
					if strings.Contains(r.URL.Path, "configuration") {
						fmt.Fprint(w, `{}`)
						return
					}
					if strings.Contains(r.URL.Path, "aliases") {
						fmt.Fprint(w, `{"Aliases":[]}`)
						return
					}
					if failure == "list-unavailable" {
						w.WriteHeader(503)
						fmt.Fprint(w, `{"message":"temporarily unavailable"}`)
						return
					}
					fmt.Fprint(w, `{"Functions":[{"FunctionName":"example","FunctionArn":"arn:aws:lambda:us-east-1:123456789012:function:example"}]}`)
					return
				}
				if strings.Contains(r.URL.Path, "tags") {
					w.Header().Set("Content-Type", "application/json")
					if failure == "tags-denied" {
						w.Header().Set("X-Amzn-Errortype", "AccessDeniedException")
						w.WriteHeader(403)
						fmt.Fprint(w, `{"message":"not authorized"}`)
						return
					}
					fmt.Fprint(w, `{"Tags":{}}`)
					return
				}
				_ = r.ParseForm()
				w.Header().Set("Content-Type", "text/xml")
				switch r.FormValue("Action") {
				case "GetCallerIdentity":
					fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/test</Arn><UserId>test</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`)
				case "DescribeRegions":
					fmt.Fprint(w, `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><regionInfo><item><regionName>us-east-1</regionName></item></regionInfo></DescribeRegionsResponse>`)
				default:
					t.Errorf("unexpected request: %s %s action=%s", r.Method, r.URL, r.FormValue("Action"))
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			t.Setenv("AWS_ENDPOINT_URL", server.URL)
			t.Setenv("AWS_MAX_ATTEMPTS", "1")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := (awsTester{}).Test(ctx, Input{BootstrapRegion: "us-east-1", Credentials: map[string]string{"accessKeyId": "test", "secretAccessKey": "test"}, Capabilities: spec.Capabilities{Workloads: []spec.WorkloadCapability{{Type: "aws.lambda", Discovery: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != (failure == "none") {
				t.Fatalf("failure=%s result=%+v checks=%+v", failure, result, result.Checks)
			}
			found := false
			for _, check := range result.Checks {
				if check.Name == CheckDiscoveryLambda {
					found = true
					if check.Passed != (failure == "none") {
						t.Fatalf("unexpected discovery result %+v", check)
					}
				}
			}
			if !found {
				t.Fatalf("discovery probe not reached: %+v", result.Checks)
			}
		})
	}
}

func TestAWSExternalIDValidation(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"existing role without external ID", "", true},
		{"minimum", "ab", true},
		{"maximum", strings.Repeat("a", 1224), true},
		{"too short", "a", false},
		{"too long", strings.Repeat("a", 1225), false},
		{"whitespace", "bad id", false},
		{"allowed punctuation", "customer+=,.@:/-", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validExternalID(tc.value); got != tc.valid {
				t.Fatalf("validExternalID() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestAWSAssumeRoleWithExternalID(t *testing.T) {
	assumed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/xml")
		switch r.FormValue("Action") {
		case "AssumeRole":
			assumed = true
			if r.FormValue("ExternalId") != "customer-specific-id" || r.FormValue("RoleArn") != "arn:aws:iam::123456789012:role/Connector" {
				t.Errorf("incorrect role request: %v", r.Form)
			}
			fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIATEST</AccessKeyId><SecretAccessKey>test-secret</SecretAccessKey><SessionToken>test-session</SessionToken><Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/Connector/test</Arn><AssumedRoleId>test</AssumedRoleId></AssumedRoleUser></AssumeRoleResult></AssumeRoleResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "GetCallerIdentity":
			fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>123456789012</Account><Arn>arn:aws:sts::123456789012:assumed-role/Connector/test</Arn><UserId>test</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`)
		default:
			t.Errorf("unexpected action: %s", r.FormValue("Action"))
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL", server.URL)
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := (awsTester{}).Test(ctx, Input{BootstrapRegion: "us-east-1", ExpectedAccountID: "123456789012", Credentials: map[string]string{"accessKeyId": "test", "secretAccessKey": "test", "roleArn": "arn:aws:iam::123456789012:role/Connector", "externalId": "customer-specific-id"}})
	if err != nil {
		t.Fatal(err)
	}
	if !assumed || !result.Success {
		t.Fatalf("assumed=%v result=%+v", assumed, result)
	}
}
