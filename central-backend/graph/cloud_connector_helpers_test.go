package graph

import (
	"reflect"
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// The Collector drawer reads these sections from cloudConnectorProviders. Keep the
// catalog boundary covered so a spec step cannot disappear or lose its snippet
// during the backend-to-UI mapping.
func TestAWSCollectorStepsReachProviderCatalog(t *testing.T) {
	declared := spec.MustGet("aws")
	provider := cloudConnectorProviderFromSpec(declared)
	if provider.Setup == nil {
		t.Fatal("AWS setup artifacts were not exposed to the UI")
	}
	for _, mode := range []string{spec.ModeAutomatic, spec.ModeManual} {
		for _, method := range []string{"", ":aws-cli", ":cloudformation"} {
			section := spec.CollectorModeSection("aws.fargate-collector", mode) + method
			want := declared.SetupArtifactsForSection(section)
			if len(want) == 0 {
				t.Fatalf("aws.yaml declares no Collector steps for %s", section)
			}
			got := make([]*model.CloudConnectorSetupArtifact, 0, len(want))
			for _, artifact := range provider.Setup.Artifacts {
				if artifact.Section == section {
					got = append(got, artifact)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("%s Collector steps in UI catalog = %d, want %d", section, len(got), len(want))
			}
			for i, artifact := range got {
				if artifact.ID != want[i].ID || artifact.Title != want[i].Title || artifact.Description != want[i].Description ||
					artifact.VariantLabel != want[i].VariantLabel || artifact.Format != want[i].Format || artifact.Template != want[i].Template ||
					artifact.ItemTemplate != want[i].ItemTemplate || artifact.ItemSeparator != want[i].ItemSeparator ||
					!reflect.DeepEqual(artifact.Steps, nonNilStrings(want[i].Steps)) {
					t.Errorf("%s Collector step %d lost spec content: got %#v, want %#v", section, i, artifact, want[i])
				}
			}
		}
	}
}

// TestCloudConnectorFieldsCarryShowInUi guards the boundary that made `showInUi: false` inert: the flag
// is declared in the provider YAML and honored by the create-connector drawer, but the only thing joining
// them is this mapper. When it dropped the flag, every declared field rendered — which is how the AWS
// assume-role panel came to show Session Token alongside the access keys.
func TestCloudConnectorFieldsCarryShowInUi(t *testing.T) {
	hidden := false
	shown := true

	fields := []spec.Field{
		{Name: "explicitlyShown", ShowInUi: &shown},
		{Name: "explicitlyHidden", ShowInUi: &hidden},
		{Name: "unspecified"},
	}

	got := cloudConnectorFields(fields)
	if len(got) != len(fields) {
		t.Fatalf("mapped %d fields, want %d", len(got), len(fields))
	}

	byName := map[string]bool{}
	for _, f := range got {
		byName[f.Name] = f.ShowInUI
	}

	if !byName["explicitlyShown"] {
		t.Error("showInUi: true should map to ShowInUI true")
	}
	if byName["explicitlyHidden"] {
		t.Error("showInUi: false should map to ShowInUI false; the flag is being dropped again")
	}
	// Default-shown is load-bearing: gcp.yaml declares the key on no field at all, and every AWS
	// connector field omits it too. A default of hidden would empty those panels.
	if !byName["unspecified"] {
		t.Error("a field that omits showInUi should stay visible")
	}
}

// TestAWSSessionTokenIsHiddenEndToEnd runs the real AWS spec through the mapper, so the assertion is
// about what the UI is actually served rather than about a synthetic field.
func TestAWSSessionTokenIsHiddenEndToEnd(t *testing.T) {
	provider := cloudConnectorProviderFromSpec(spec.MustGet("aws"))

	var staticKeys *struct{ shown, hidden []string }
	for _, m := range provider.Auth.Methods {
		if m.ID != "static-keys" {
			continue
		}
		staticKeys = &struct{ shown, hidden []string }{}
		for _, f := range m.Fields {
			if f.ShowInUI {
				staticKeys.shown = append(staticKeys.shown, f.Name)
			} else {
				staticKeys.hidden = append(staticKeys.hidden, f.Name)
			}
		}
	}
	if staticKeys == nil {
		t.Fatal("aws spec declares no static-keys auth method")
	}

	if want := []string{"sessionToken"}; !equalStrings(staticKeys.hidden, want) {
		t.Errorf("static-keys hidden fields = %v, want %v", staticKeys.hidden, want)
	}
	if want := []string{"accessKeyId", "secretAccessKey"}; !equalStrings(staticKeys.shown, want) {
		t.Errorf("static-keys shown fields = %v, want %v", staticKeys.shown, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// featureByKey finds an emitted feature, so a test can assert on one without depending on list order.
func featureByKey(fs []*model.CloudConnectorFeature, key string) *model.CloudConnectorFeature {
	for _, f := range fs {
		if f != nil && f.Key == key {
			return f
		}
	}
	return nil
}

func hasPermission(f *model.CloudConnectorFeature, want string) bool {
	if f == nil {
		return false
	}
	for _, p := range f.Permissions {
		if p == want {
			return true
		}
	}
	for _, t := range f.Types {
		if t == nil {
			continue
		}
		for _, p := range t.Permissions {
			if p == want {
				return true
			}
		}
	}
	return false
}

// TestCollectorFeaturesReachTheCatalog: a managed Collector needs permissions the customer must grant and
// Odigos cannot infer, so both halves of the domain have to be reachable by the client that renders the
// policy. This mapper is the only thing joining the provider YAML to that client — the same boundary
// TestCloudConnectorFieldsCarryShowInUi guards for fields.
func TestCollectorFeaturesReachTheCatalog(t *testing.T) {
	// AWS does not declare a Collector implementation until the later AWS stack. Keep this
	// boundary test independent of that future provider work: its contract is that a provider
	// declaration reaches the GraphQL catalog intact.
	features := cloudConnectorFeatures(spec.CloudConnectorSpec{Collectors: spec.CollectorDomain{
		Management: spec.Feature{
			Enabled:     true,
			Permissions: []string{"ecs:DescribeServices"},
		},
		Deployment: spec.Feature{
			Enabled:     true,
			Permissions: []string{"ecs:CreateService"},
		},
	}})

	mgmt := featureByKey(features, featureCollectorManagement)
	if mgmt == nil {
		t.Fatalf("no %q feature emitted; a customer would never be told what a Collector needs to be observed", featureCollectorManagement)
	}
	dep := featureByKey(features, featureCollectorDeployment)
	if dep == nil {
		t.Fatalf("no %q feature emitted; a customer would apply a policy with no Collector write permissions and the deployer would fail on its first call", featureCollectorDeployment)
	}

	// The observe/mutate split has to survive the mapping, or manual mode stops being observe-only at
	// exactly the point a customer reads the policy.
	if !hasPermission(mgmt, "ecs:DescribeServices") {
		t.Error("collector management carries no observation read")
	}
	if hasPermission(mgmt, "ecs:CreateService") {
		t.Error("collector management carries a mutating permission; manual mode must stay observe-only")
	}
	if !hasPermission(dep, "ecs:CreateService") {
		t.Error("collector deployment carries no mutating permission")
	}
}

// TestCollectorFeaturesDoNotDisturbTheExistingCatalog is the safety half. ui-kit aggregates permissions
// only for the feature keys named in the selected access level's allowlist, so adding a domain must
// change nothing a current client sees: the four keys it knows must keep their exact identity and
// permissions, and the two new keys must be genuinely new rather than colliding with one of them.
func TestCollectorFeaturesDoNotDisturbTheExistingCatalog(t *testing.T) {
	for _, provider := range spec.Providers() {
		s := spec.MustGet(provider)
		features := cloudConnectorFeatures(s)

		for key, want := range map[string]spec.Feature{
			featureDiscovery:        s.Workloads.Discovery,
			featureInstrumentation:  s.Workloads.Instrumentation,
			featureComputePlatforms: s.ComputePlatforms.Discovery,
			featureGateway:          s.Gateway,
		} {
			got := featureByKey(features, key)
			if got == nil {
				t.Errorf("%s: pre-existing feature %q disappeared from the catalog", provider, key)
				continue
			}
			if len(got.Permissions) != len(want.Permissions) || len(got.Types) != len(want.Types) {
				t.Errorf("%s: feature %q changed shape: %d perms/%d types, want %d/%d",
					provider, key, len(got.Permissions), len(got.Types), len(want.Permissions), len(want.Types))
			}
		}

		// Distinct keys, or an existing access level would silently start aggregating Collector
		// permissions into a policy the customer did not ask for.
		seen := map[string]bool{}
		for _, f := range features {
			if f == nil {
				continue
			}
			if seen[f.Key] {
				t.Errorf("%s: duplicate feature key %q", provider, f.Key)
			}
			seen[f.Key] = true
		}
		for _, k := range []string{featureCollectorManagement, featureCollectorDeployment} {
			for _, old := range []string{featureDiscovery, featureInstrumentation, featureComputePlatforms, featureGateway} {
				if k == old {
					t.Errorf("collector feature key %q collides with the pre-existing %q", k, old)
				}
			}
		}
	}
}

// TestProvidersWithoutCollectorsEmitAnEmptyFeature: Azure and GCP declare no collectors domain yet. They
// must still emit the feature — disabled and empty — rather than nothing, so a client can render the
// same shape for every provider instead of branching on which ones have the domain.
func TestProvidersWithoutCollectorsEmitAnEmptyFeature(t *testing.T) {
	for _, provider := range []string{"azure", "gcp"} {
		s := spec.MustGet(provider)
		if len(s.Collectors.Management.Types) != 0 {
			continue // this provider has since declared the domain; nothing to assert here
		}
		f := featureByKey(cloudConnectorFeatures(s), featureCollectorManagement)
		if f == nil {
			t.Errorf("%s: no collector management feature emitted at all", provider)
			continue
		}
		if f.Enabled {
			t.Errorf("%s: collector management reports enabled with no types declared", provider)
		}
		if len(f.Types) != 0 {
			t.Errorf("%s: collector management emitted %d types, want none", provider, len(f.Types))
		}
	}
}
