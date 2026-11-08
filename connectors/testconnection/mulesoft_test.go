package testconnection

import (
	"context"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint/fake"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

func muleInput(srv *fake.Server, secret string) Input {
	return Input{
		Provider:          "mulesoft",
		BootstrapRegion:   "us",
		ExpectedAccountID: fake.FixtureOrgID,
		Credentials: map[string]string{
			credMuleClientID:     fake.FixtureClientID,
			credMuleClientSecret: secret,
		},
		Capabilities: spec.Capabilities{Workloads: []spec.WorkloadCapability{
			{Type: typeMuleCloudHub2, Discovery: true, Instrumentation: true},
			{Type: typeMulePrivateSpace, Discovery: true},
			{Type: typeMuleRTF, Discovery: true},
			{Type: typeMuleHybrid, Discovery: true},
			{Type: typeMuleOmniGateway, Discovery: true, Instrumentation: true},
		}},
	}
}

func checkByName(res *Result, name CheckName) (CheckResult, bool) {
	for _, c := range res.Checks {
		if c.Name == name {
			return c, true
		}
	}
	return CheckResult{}, false
}

func TestMuleSoftTestConnectionPasses(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()

	res, err := muleSoftTester{baseURL: srv.URL}.Test(context.Background(), muleInput(srv, fake.FixtureClientSecret))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %+v", res.Checks)
	}
	if res.AccountID != fake.FixtureOrgID {
		t.Errorf("account id = %q, want %q", res.AccountID, fake.FixtureOrgID)
	}
	for _, name := range []CheckName{CheckMuleAuthToken, CheckMuleOrganizationMatch, CheckMuleEnvironments,
		CheckMuleDiscoveryCloudHub2, CheckMuleDiscoveryPrivateSpace, CheckMuleDiscoveryRTF, CheckMuleDiscoveryHybrid,
		CheckMuleDiscoveryGateway} {
		if _, ok := checkByName(res, name); !ok {
			t.Errorf("missing check %s", name)
		}
	}
}

func TestMuleSoftTestConnectionWrongSecret(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	const secret = "wrong-secret-value"

	in := muleInput(srv, secret)
	res, err := muleSoftTester{baseURL: srv.URL}.Test(context.Background(), in)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.Success {
		t.Fatal("a wrong secret must fail")
	}
	c, ok := checkByName(res, CheckMuleAuthToken)
	if !ok || c.Passed {
		t.Fatalf("auth check = %+v, want a failure", c)
	}
	for _, c := range res.Checks {
		if strings.Contains(c.Message, secret) || strings.Contains(c.Remediation, secret) {
			t.Fatalf("check %s leaks the secret: %+v", c.Name, c)
		}
	}
}

func TestMuleSoftTestConnectionMissingScope(t *testing.T) {
	st := fake.SampleState()
	st.Forbidden = []string{"/amc/"}
	srv := fake.NewServer(st)
	defer srv.Close()

	res, err := muleSoftTester{baseURL: srv.URL}.Test(context.Background(), muleInput(srv, fake.FixtureClientSecret))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.Success {
		t.Fatal("a Connected App without Read Applications must fail")
	}
	c, ok := checkByName(res, CheckMuleDiscoveryCloudHub2)
	if !ok || c.Passed || !strings.Contains(c.Remediation, "Read Applications") {
		t.Fatalf("cloudhub2 check = %+v, want a failure naming the missing scope", c)
	}
	if h, _ := checkByName(res, CheckMuleDiscoveryHybrid); !h.Passed {
		t.Errorf("hybrid discovery does not use the AMC API and should still pass, got %+v", h)
	}
}

func TestMuleSoftTestConnectionNoEnvironments(t *testing.T) {
	st := fake.SampleState()
	st.Environments = nil
	srv := fake.NewServer(st)
	defer srv.Close()

	res, err := muleSoftTester{baseURL: srv.URL}.Test(context.Background(), muleInput(srv, fake.FixtureClientSecret))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	c, ok := checkByName(res, CheckMuleEnvironments)
	if res.Success || !ok || c.Passed {
		t.Fatalf("environments check = %+v (success %v), want a failure", c, res.Success)
	}
}

func TestMuleSoftTestConnectionRejectsUnknownControlPlane(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	in := muleInput(srv, fake.FixtureClientSecret)
	in.BootstrapRegion = "gov"

	res, err := muleSoftTester{}.Test(context.Background(), in)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.Success {
		t.Fatal("an unsupported control plane must fail")
	}
}

// TestMuleSoftTestConnectionIgnoresARequestSuppliedHost is the SSRF guard: test connection runs in Central's
// backend for any signed-in user, so a credential naming another API host must be refused, never followed.
func TestMuleSoftTestConnectionIgnoresARequestSuppliedHost(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	in := muleInput(srv, fake.FixtureClientSecret)
	in.Credentials["baseUrl"] = srv.URL

	res, err := muleSoftTester{}.Test(context.Background(), in)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.Success {
		t.Fatal("a request-supplied API host must be rejected")
	}
	if srv.Handler().TokensIssued() != 0 {
		t.Fatal("the tester contacted the request-supplied host")
	}
}

// TestMuleSoftTestConnectionSkipsAnUnscopedEnvironment: a Connected App scoped to only some environments passes
// as long as one visible environment is readable.
func TestMuleSoftTestConnectionSkipsAnUnscopedEnvironment(t *testing.T) {
	st := fake.SampleState()
	st.Forbidden = []string{"/amc/application-manager/api/v2/organizations/" + fake.FixtureOrgID + "/environments/" + fake.FixtureEnvSandbox}
	srv := fake.NewServer(st)
	defer srv.Close()

	res, err := muleSoftTester{baseURL: srv.URL}.Test(context.Background(), muleInput(srv, fake.FixtureClientSecret))
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	c, _ := checkByName(res, CheckMuleDiscoveryCloudHub2)
	if !c.Passed || !strings.Contains(c.Message, "Production") {
		t.Fatalf("cloudhub2 check = %+v, want it to pass on the Production environment", c)
	}
	if !res.Success {
		t.Errorf("expected success, got %+v", res.Checks)
	}
}
