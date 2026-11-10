package instrumentor

import (
	"context"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	"github.com/odigos-io/odigos-enterprise/connectors/anypoint/fake"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// appliedManually is what a customer gets by following the rendered instructions exactly.
func appliedManually(t *testing.T, app store.Application, dest domaindest.OTLPTarget, metrics bool) map[string]string {
	t.Helper()
	values := ManualValues(app, fake.FixtureOrgID, dest, true, 1)
	values[plan.PlaceholderServiceName] = app.AppName
	pl, err := ManualPlan(app.Kind, SignalsFor(dest, true), metrics)
	if err != nil {
		t.Fatal(err)
	}
	props := map[string]string{}
	for _, e := range pl.Fragments[0].Env {
		props[e.Name] = plan.Substitute(e.Value, values)
	}
	return props
}

func TestJudge(t *testing.T) {
	app := store.Application{ExternalID: "d", Kind: store.KindCloudHub2, AppName: "order-api", EnvironmentName: "Sandbox", TargetType: "shared-space"}
	values := ManualValues(app, fake.FixtureOrgID, grpcDest, true, 1)
	values[plan.PlaceholderServiceName] = "order-api"
	pl, _ := ManualPlan(store.KindCloudHub2, SignalsFor(grpcDest, true), true)
	good := appliedManually(t, app, grpcDest, true)

	if v, _ := Judge(pl, values, map[string]string{"inventory.url": "x"}, nil); v != baseapi.ConformanceAbsent {
		t.Errorf("no marker = %q, want Absent", v)
	}
	if v, m := Judge(pl, values, good, nil); v != baseapi.ConformanceConformant || len(m) != 0 {
		t.Errorf("applied exactly = %q %v, want Conformant", v, m)
	}

	withCustomerAttrs := copyMap(good)
	withCustomerAttrs[PropResourceAttributes] = "team=payments," + good[PropResourceAttributes]
	withCustomerAttrs["mule.openTelemetry.tracer.level"] = "DEBUG"
	if v, _ := Judge(pl, values, withCustomerAttrs, nil); v != baseapi.ConformanceConformant {
		t.Errorf("customer's own attributes and tuning must not count against them, got %q", v)
	}

	wrong := copyMap(good)
	wrong[PropTracesEndpoint] = "https://somewhere-else:4317"
	wrong[PropServiceName] = "other"
	v, m := Judge(pl, values, wrong, nil)
	if v != baseapi.ConformancePartial || len(m) != 2 || m[0] != baseapi.ComponentCollectorExport || m[1] != baseapi.ComponentServiceName {
		t.Errorf("wrong endpoint and name = %q %v", v, m)
	}

	secure := copyMap(good)
	delete(secure, PropTracesEndpoint)
	if v, _ := Judge(pl, values, secure, []string{PropTracesEndpoint}); v != baseapi.ConformanceConformant {
		t.Errorf("a secure property is present even though its value cannot be read, got %q", v)
	}
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

type obsInventory struct {
	*memInventory
	observed map[string]store.Observation
}

func (o *obsInventory) SetObservation(_ context.Context, id string, obs store.Observation) error {
	o.observed[id] = obs
	o.apps[id].Observation = obs // read back on the next pass, as the store's ListApplications does
	return nil
}

func TestObserverRecordsAVerdictPerEnabledApplication(t *testing.T) {
	st := fake.SampleState()
	app := store.Application{ExternalID: fake.FixtureOrderSandbox, Kind: store.KindCloudHub2, AppName: "order-api",
		EnvironmentID: fake.FixtureEnvSandbox, EnvironmentName: "Sandbox", TargetType: anypoint.TargetSharedSpace}
	// order-api in Sandbox: the customer applied the instructions. order-api in Production: nothing yet.
	props := appliedManually(t, app, grpcDest, true)
	props["inventory.url"] = "http://inventory-api"
	st.Deployments[fake.FixtureEnvSandbox][0].Application.Configuration.PropertiesService.Properties = props

	srv := fake.NewServer(st)
	t.Cleanup(srv.Close)
	client := anypoint.New(anypoint.Config{BaseURL: srv.URL, ClientID: fake.FixtureClientID, ClientSecret: fake.FixtureClientSecret, Backoff: time.Millisecond})
	mem := &memInventory{apps: map[string]*store.Application{}, dest: grpcDest, hasDest: true}
	mem.apps[app.ExternalID] = &app
	prod := app
	prod.ExternalID, prod.EnvironmentID, prod.EnvironmentName = fake.FixtureOrderProduction, fake.FixtureEnvProduction, "Production"
	mem.apps[prod.ExternalID] = &prod
	mem.sources = []basestore.SourceRecord{
		{Name: "order-api", Kind: store.KindCloudHub2, Enabled: true, ExternalResourceID: app.ExternalID},
		{Name: "order-api", Kind: store.KindCloudHub2, Enabled: true, ExternalResourceID: prod.ExternalID},
	}
	inv := &obsInventory{memInventory: mem, observed: map[string]store.Observation{}}
	o := NewObserver(store.KindCloudHub2, inv, client, fake.FixtureOrgID, func() Settings { return Settings{SampleRatio: 1} })

	if err := o.Observe(context.Background()); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if got := inv.observed[app.ExternalID]; got.Conformance != baseapi.ConformanceConformant || got.Since.IsZero() {
		t.Errorf("sandbox = %+v, want Conformant with a since time", got)
	}
	if !mem.apps[app.ExternalID].Instrumented || mem.apps[app.ExternalID].Origin != store.OriginExternal {
		t.Error("the observer should record the customer's marker as external instrumentation")
	}
	if got := inv.observed[prod.ExternalID]; got.Conformance != baseapi.ConformanceAbsent {
		t.Errorf("production = %+v, want Absent", got)
	}
	if n := len(srv.Handler().Patches()); n != 0 {
		t.Fatalf("the observer is read-only, got %d patches", n)
	}

	// No destination: the export settings cannot be judged, which is a reason, not drift.
	mem.hasDest = false
	if err := o.Observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inv.observed[app.ExternalID]; got.Error == "" || got.Conformance != baseapi.ConformanceConformant {
		t.Errorf("without a destination = %+v, want the last verdict kept with an error", got)
	}
}

// TestTracesOnlyDestinationIsConformant: the instructions for a destination that takes only traces ask for only
// the trace properties, and a customer who applied exactly those is Conformant — not Partial for logs and
// metrics the destination would drop.
func TestTracesOnlyDestinationIsConformant(t *testing.T) {
	app := store.Application{ExternalID: "d", Kind: store.KindCloudHub2, AppName: "order-api", EnvironmentName: "Sandbox", TargetType: "shared-space"}
	tracesOnly := domaindest.OTLPTarget{Endpoint: grpcDest.Endpoint, Traces: true}
	applied := appliedManually(t, app, tracesOnly, true)
	if _, ok := applied[PropLogsEnabled]; ok {
		t.Fatal("traces-only instructions must not ask for logs")
	}
	values := ManualValues(app, fake.FixtureOrgID, tracesOnly, true, 1)
	values[plan.PlaceholderServiceName] = "order-api"
	pl, _ := ManualPlan(app.Kind, SignalsFor(tracesOnly, true), true)
	if v, m := Judge(pl, values, applied, nil); v != baseapi.ConformanceConformant {
		t.Errorf("traces-only applied = %q %v, want Conformant", v, m)
	}
}
