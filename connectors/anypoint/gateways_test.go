package anypoint_test

import (
	"context"
	"errors"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	"github.com/odigos-io/odigos-enterprise/connectors/anypoint/fake"
)

// API Manager sends instance ids as numbers and groups instances under their Exchange asset; an application's
// autodiscovery names the id as a string. The listing must come back flat, with string ids that compare equal.
func TestListAPIInstancesFlattensAssetsAndReadsNumericIDs(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	c := newClient(t, srv, fake.FixtureClientSecret)

	got, err := c.ListAPIInstances(context.Background(), fake.FixtureOrgID, fake.FixtureEnvProduction)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("instances = %+v, want 1", got)
	}
	a := got[0]
	if a.ID != fake.FixtureOrderAPIInstance || a.AssetID != "order-api" || a.AssetName != "Order API" || a.ProductVersion != "v1" || a.Technology != "mule4" {
		t.Errorf("instance = %+v", a)
	}
}

// The listing pages until a page comes back empty, so every instance is read whatever unit the server counts
// its limit in.
func TestListAPIInstancesReadsEveryPage(t *testing.T) {
	st := fake.SampleState()
	st.APIs[fake.FixtureEnvSandbox] = nil
	for i := range 250 {
		st.APIs[fake.FixtureEnvSandbox] = append(st.APIs[fake.FixtureEnvSandbox], anypoint.APIInstance{
			ID: itoa(1000 + i), AssetID: "asset-" + itoa(i%7), ProductVersion: "v1",
		})
	}
	srv := fake.NewServer(st)
	defer srv.Close()
	got, err := newClient(t, srv, fake.FixtureClientSecret).ListAPIInstances(context.Background(), fake.FixtureOrgID, fake.FixtureEnvSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 250 {
		t.Errorf("instances = %d, want 250", len(got))
	}
}

func TestGatewaysListGetAndUpdate(t *testing.T) {
	srv := fake.NewServer(fake.SampleState())
	defer srv.Close()
	c := newClient(t, srv, fake.FixtureClientSecret)
	ctx := context.Background()

	gws, err := c.ListGateways(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox)
	if err != nil {
		t.Fatal(err)
	}
	managed := map[string]bool{}
	for _, g := range gws {
		managed[g.ID] = g.Managed()
	}
	if len(gws) != 2 || !managed[fake.FixtureManagedGateway] || managed[fake.FixtureSelfGateway] {
		t.Fatalf("gateways = %+v, want the managed one managed and the self-managed one not", gws)
	}

	d, err := c.GetGateway(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, fake.FixtureManagedGateway)
	if err != nil {
		t.Fatal(err)
	}
	if d.Tracing() != nil {
		t.Fatalf("tracing = %v, want none on the fixture", d.Tracing())
	}
	body := d.UpdateBody(map[string]any{"enabled": true})
	if body["name"] != "odigos-gateway-1" || body["size"] != "small" {
		t.Errorf("update body lost the gateway's settings: %v", body)
	}
	cfg := body["configuration"].(map[string]any)
	if _, ok := cfg["ingress"]; !ok {
		t.Error("update body dropped configuration it does not model (ingress)")
	}
	if err := c.UpdateGateway(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, fake.FixtureManagedGateway, body); err != nil {
		t.Fatal(err)
	}
	after, err := c.GetGateway(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, fake.FixtureManagedGateway)
	if err != nil {
		t.Fatal(err)
	}
	if after.Tracing()["enabled"] != true {
		t.Errorf("tracing after update = %v", after.Tracing())
	}

	// A self-managed gateway's configuration lives on its host; Gateway Manager does not take it.
	err = c.UpdateGateway(ctx, fake.FixtureOrgID, fake.FixtureEnvSandbox, fake.FixtureSelfGateway, body)
	var apiErr *anypoint.APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("updating a self-managed gateway: err = %v, want an API error", err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{digits[i%10]}, b...)
	}
	return string(b)
}
