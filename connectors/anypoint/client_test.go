package anypoint_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	"github.com/odigos-io/odigos-enterprise/connectors/anypoint/fake"
)

func newClient(t *testing.T, srv *fake.Server, secret string) *anypoint.Client {
	t.Helper()
	return anypoint.New(anypoint.Config{
		BaseURL:      srv.URL,
		ClientID:     fake.FixtureClientID,
		ClientSecret: secret,
		Backoff:      time.Millisecond,
	})
}

func TestTokenIsCachedAcrossCalls(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	c := newClient(t, srv, fake.FixtureClientSecret)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Environments(ctx, fake.FixtureOrgID); err != nil {
			t.Fatalf("environments: %v", err)
		}
	}
	if got := srv.Handler().TokensIssued(); got != 1 {
		t.Fatalf("tokens issued = %d, want 1 (the token must be reused until it nears expiry)", got)
	}
}

func TestWrongSecretIsAnAuthErrorThatDoesNotEchoTheSecret(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	const secret = "s3cr3t-value-that-must-not-leak"
	c := newClient(t, srv, secret)

	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected an error for a wrong client secret")
	}
	if !anypoint.IsAuthErr(err) {
		t.Fatalf("IsAuthErr(%v) = false, want true", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(anypoint.Summarize(err), secret) {
		t.Fatalf("error leaks the client secret: %v", err)
	}
}

func TestThrottledRequestsAreRetried(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	srv.Handler().ThrottleNext = 2
	c := newClient(t, srv, fake.FixtureClientSecret)

	envs, err := c.Environments(context.Background(), fake.FixtureOrgID)
	if err != nil {
		t.Fatalf("environments after throttling: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("environments = %d, want 2", len(envs))
	}
}

func TestForbiddenIsAnAuthError(t *testing.T) {
	st := fake.SampleState()
	st.Forbidden = []string{"/amc/"}
	srv := fake.NewServer(st)
	defer srv.Close()
	c := newClient(t, srv, fake.FixtureClientSecret)

	_, err := c.ListDeployments(context.Background(), fake.FixtureOrgID, fake.FixtureEnvSandbox)
	if !anypoint.IsAuthErr(err) || anypoint.StatusCode(err) != 403 {
		t.Fatalf("ListDeployments error = %v, want a 403 auth error", err)
	}
}

func TestMeReportsTheConnectedAppOrganization(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	id, err := newClient(t, srv, fake.FixtureClientSecret).Me(context.Background())
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	if id.OrgID != fake.FixtureOrgID {
		t.Fatalf("org = %q, want %q", id.OrgID, fake.FixtureOrgID)
	}
}

func TestListIsASummaryAndGetCarriesProperties(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	c := newClient(t, srv, fake.FixtureClientSecret)
	ctx := context.Background()

	list, err := c.ListDeployments(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 || len(list[0].Properties()) != 0 {
		t.Fatalf("list view should omit properties, got %+v", list)
	}
	d, err := c.GetDeployment(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, fake.FixtureOrderSandbox)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if d.Properties()["inventory.url"] != "http://inventory-api" {
		t.Fatalf("get should carry properties, got %v", d.Properties())
	}
}

func TestUpdatePropertiesSendsTheWholeMapAndKeepsSecureProperties(t *testing.T) {
	st := fake.SampleState()
	dep := st.Deployments[fake.FixtureEnvSandbox][0]
	dep.Application.Configuration.PropertiesService.SecureProperties = map[string]string{"db.password": ""}
	st.Deployments[fake.FixtureEnvSandbox][0] = dep
	srv := fake.NewServer(st)
	defer srv.Close()
	c := newClient(t, srv, fake.FixtureClientSecret)
	ctx := context.Background()

	props := map[string]string{"inventory.url": "http://inventory-api", "a": "b"}
	if err := c.UpdateDeploymentProperties(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, dep.ID, dep.Name, props); err != nil {
		t.Fatalf("update: %v", err)
	}
	patches := srv.Handler().Patches()
	if len(patches) != 1 || patches[0].Properties["inventory.url"] == "" || patches[0].Properties["a"] != "b" {
		t.Fatalf("patch = %+v, want the full property map", patches)
	}
	got, err := c.GetDeployment(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, dep.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if keys := got.SecurePropertyKeys(); len(keys) != 1 || keys[0] != "db.password" {
		t.Fatalf("secure property keys = %v, want [db.password] preserved", keys)
	}
}

func TestBaseURLFor(t *testing.T) {
	for _, tc := range []struct{ plane, override, want string }{
		{"us", "", "https://anypoint.mulesoft.com"},
		{"eu", "", "https://eu1.anypoint.mulesoft.com"},
		{"us", "http://fake:8080/", "http://fake:8080"},
	} {
		got, err := anypoint.BaseURLFor(tc.plane, tc.override)
		if err != nil || got != tc.want {
			t.Errorf("BaseURLFor(%q, %q) = %q, %v; want %q", tc.plane, tc.override, got, err, tc.want)
		}
	}
	if _, err := anypoint.BaseURLFor("gov", ""); err == nil {
		t.Error("an unsupported control plane must be rejected")
	}
}

func TestParseMuleVersion(t *testing.T) {
	for _, tc := range []struct {
		in         string
		raw, java  string
		ok, has411 bool
	}{
		{"4.11.0:1e-java17", "4.11.0", "17", true, true},
		{"4.12.1:2e-java17", "4.12.1", "17", true, true},
		{"4.10.3:5e-java8", "4.10.3", "8", true, false},
		{"4.11.1", "4.11.1", "", true, true},
		{"4.9", "4.9.0", "", true, false},
		{"5.0.0", "5.0.0", "", true, true},
		{"", "", "", false, false},
		{"latest", "", "", false, false},
	} {
		v, ok := anypoint.ParseMuleVersion(tc.in)
		if ok != tc.ok {
			t.Errorf("ParseMuleVersion(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if v.Raw != tc.raw || v.Java != tc.java || v.AtLeast(4, 11) != tc.has411 {
			t.Errorf("ParseMuleVersion(%q) = %+v (>=4.11: %v), want raw %q java %q >=4.11 %v",
				tc.in, v, v.AtLeast(4, 11), tc.raw, tc.java, tc.has411)
		}
	}
}

func TestTruncatedListsReturnTheItemsWithErrIncomplete(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	srv.Handler().InflateTotals = 5
	c := newClient(t, srv, fake.FixtureClientSecret)

	deps, err := c.ListDeployments(context.Background(), fake.FixtureOrgID, fake.FixtureEnvSandbox)
	if !errors.Is(err, anypoint.ErrIncomplete) || len(deps) == 0 {
		t.Fatalf("ListDeployments = %d items, %v; want the items and ErrIncomplete", len(deps), err)
	}
	envs, err := c.Environments(context.Background(), fake.FixtureOrgID)
	if !errors.Is(err, anypoint.ErrIncomplete) || len(envs) != 2 {
		t.Fatalf("Environments = %d items, %v; want the items and ErrIncomplete", len(envs), err)
	}
}
