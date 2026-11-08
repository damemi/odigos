package fake

import "github.com/odigos-io/odigos-enterprise/connectors/anypoint"

// Fixture ids, exported so tests can address the sample content by name.
const (
	FixtureOrgID        = "0e1f5c6a-1111-4c2b-9d3e-000000000001"
	FixtureClientID     = "fake-client-id"
	FixtureClientSecret = "fake-client-secret"

	FixtureEnvSandbox    = "env-sandbox"
	FixtureEnvProduction = "env-production"

	FixtureTargetShared  = "cloudhub-us-east-2"
	FixtureTargetPrivate = "ps-0001"
	FixtureTargetRTF     = "rtf-0001"

	// One app name in two environments: the identity collision the connector must keep apart.
	FixtureOrderSandbox    = "dep-order-sandbox"
	FixtureOrderProduction = "dep-order-production"
	FixtureInventory       = "dep-inventory-private"
	FixtureRTFApp          = "dep-billing-rtf"
	FixtureLegacyApp       = "dep-legacy-410"
	FixtureConflictApp     = "dep-conflict"

	// FixtureOrderAPIInstance is the API Manager instance the production order-api pairs with through
	// autodiscovery (its api.id property); FixtureGatewayAPIInstance is published on the managed gateway.
	FixtureOrderAPIInstance   = "18977143"
	FixtureGatewayAPIInstance = "18977150"

	// One Managed Omni Gateway (run by MuleSoft in the shared space) and one self-managed gateway.
	FixtureManagedGateway = "gw-managed-0001"
	FixtureSelfGateway    = "gw-self-0001"
)

func deployment(id, name, target, runtime string, props map[string]string) anypoint.Deployment {
	if props == nil {
		props = map[string]string{}
	}
	return anypoint.Deployment{
		ID:                    id,
		Name:                  name,
		Status:                anypoint.DeploymentApplied,
		CurrentRuntimeVersion: runtime,
		Application: anypoint.DeploymentApplication{
			Status:       "RUNNING",
			DesiredState: "STARTED",
			Configuration: &anypoint.ApplicationConfiguration{
				PropertiesService: &anypoint.PropertiesService{ApplicationName: name, Properties: props},
			},
		},
		Target: anypoint.DeploymentTarget{Provider: "MC", TargetID: target, Replicas: 1},
	}
}

// SampleState is an organization covering every case the connector distinguishes: CloudHub 2.0 shared and
// private spaces, Runtime Fabric and hybrid; one app name in two environments; an ineligible runtime; and an
// app that already carries a customer-owned telemetry property.
func SampleState() State {
	return State{
		ClientID:     FixtureClientID,
		ClientSecret: FixtureClientSecret,
		OrgID:        FixtureOrgID,
		ClientName:   "odigos-connector",
		Environments: []anypoint.Environment{
			{ID: FixtureEnvSandbox, Name: "Sandbox", OrganizationID: FixtureOrgID, Type: "sandbox"},
			{ID: FixtureEnvProduction, Name: "Production", OrganizationID: FixtureOrgID, Type: "production", IsProduction: true},
		},
		Targets: []anypoint.Target{
			{ID: FixtureTargetShared, Name: "Cloudhub-US-East-2", Type: "shared-space", Region: "us-east-2"},
			{ID: FixtureTargetPrivate, Name: "odigos-private", Type: "private-space", Region: "us-east-1"},
			{ID: FixtureTargetRTF, Name: "odigos-rtf", Type: "runtime-fabric", Region: "eu-west-1"},
		},
		Deployments: map[string][]anypoint.Deployment{
			FixtureEnvSandbox: {
				deployment(FixtureOrderSandbox, "order-api", FixtureTargetShared, "4.12.0:2e-java17", map[string]string{"inventory.url": "http://inventory-api"}),
				deployment(FixtureLegacyApp, "legacy-api", FixtureTargetShared, "4.10.3:5e-java17", nil),
				deployment(FixtureConflictApp, "conflict-api", FixtureTargetShared, "4.12.0:2e-java17", map[string]string{
					"mule.openTelemetry.tracer.exporter.endpoint": "https://otel.example.com:4318/v1/traces",
				}),
			},
			FixtureEnvProduction: {
				deployment(FixtureOrderProduction, "order-api", FixtureTargetShared, "4.11.2:1e-java17", map[string]string{
					"api.id": FixtureOrderAPIInstance,
				}),
				deployment(FixtureInventory, "inventory-api", FixtureTargetPrivate, "4.12.0:2e-java17", nil),
				deployment(FixtureRTFApp, "billing-api", FixtureTargetRTF, "4.12.1:1e-java17", nil),
			},
		},
		HybridApps: map[string][]anypoint.HybridApplication{
			FixtureEnvProduction: {
				{ID: 4101, Name: "ledger-api", DesiredStatus: "STARTED", LastReportedStatus: "STARTED",
					Target: anypoint.HybridTarget{ID: 77, Name: "dc1-mule-01", Type: "SERVER"}},
			},
		},
		HybridServers: map[string][]anypoint.HybridServer{
			FixtureEnvProduction: {{ID: 77, Name: "dc1-mule-01", Status: "RUNNING", MuleVersion: "4.11.1"}},
		},
		APIs: map[string][]anypoint.APIInstance{
			FixtureEnvProduction: {
				{ID: FixtureOrderAPIInstance, AssetID: "order-api", AssetName: "Order API", AssetVersion: "1.0.2", ProductVersion: "v1", Label: "order-api", Technology: "mule4"},
			},
			FixtureEnvSandbox: {
				{ID: FixtureGatewayAPIInstance, AssetID: "inventory-gateway-api", AssetName: "Inventory Gateway API", AssetVersion: "1.0.0", ProductVersion: "v1", Label: "inventory-gateway-api@odigos-gateway-1", Technology: "flexGateway"},
			},
		},
		Gateways: map[string][]map[string]any{
			FixtureEnvSandbox: {
				{
					"id": FixtureManagedGateway, "name": "odigos-gateway-1", "kind": "managed",
					"targetId": FixtureTargetShared, "targetName": "Cloudhub-US-East-2", "targetType": "shared-space",
					"status": "RUNNING", "ready": true, "running": true, "runtimeVersion": "1.10.2", "releaseChannel": "edge", "size": "small",
					"configuration": map[string]any{
						"ingress":    map[string]any{"publicUrl": "https://odigos-gateway-1.example.cloudhub.io", "forwardSslSession": false, "lastMileSecurity": false},
						"logging":    map[string]any{"level": "info", "forwardLogs": true},
						"properties": map[string]any{"connectionIdleTimeout": 60, "upstreamResponseTimeout": 15},
					},
				},
				{"id": FixtureSelfGateway, "name": "local-gateway", "kind": "selfManaged", "status": "CONNECTED", "runtimeVersion": "1.10.2"},
			},
		},
	}
}
