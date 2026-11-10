package awsclient

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/config"
)

// An empty expected account id means the boundary cannot be enforced — VerifyAccount must reject it
// outright (before any STS call) rather than silently allowing the connector to manage any account.
func TestVerifyAccountRejectsEmptyExpected(t *testing.T) {
	c := NewCache(config.Credentials{}, "")
	err := c.VerifyAccount(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for an empty account id, got nil")
	}
	if !strings.Contains(err.Error(), "account id") {
		t.Errorf("error = %q, want it to mention the missing account id", err.Error())
	}
}

func TestBootstrapCallsUseSelectedRegion(t *testing.T) {
	const region = "eu-west-1"
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "/"+region+"/") {
			t.Errorf("request signed for wrong region")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		action := r.Form.Get("Action")
		actions = append(actions, action)
		w.Header().Set("Content-Type", "text/xml")
		switch action {
		case "GetCallerIdentity":
			fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/test</Arn><UserId>test</UserId></GetCallerIdentityResult><ResponseMetadata><RequestId>test</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
		case "DescribeRegions":
			fmt.Fprint(w, `<DescribeRegionsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>test</requestId><regionInfo><item><regionName>eu-west-1</regionName></item><item><regionName>us-west-2</regionName></item></regionInfo></DescribeRegionsResponse>`)
		default:
			t.Errorf("unexpected action %q", action)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	c := NewCache(config.Credentials{}, region)
	c.byRegion = map[string]aws.Config{region: {Region: region, BaseEndpoint: aws.String(server.URL), Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: server.Client()}}
	if err := c.VerifyAccount(context.Background(), "123456789012"); err != nil {
		t.Fatal(err)
	}
	regions, err := c.EnabledRegions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[1] != "us-west-2" {
		t.Fatalf("discovery scope was restricted: %v", regions)
	}
	if len(actions) != 2 {
		t.Fatalf("expected two bootstrap calls, got %v", actions)
	}
}
