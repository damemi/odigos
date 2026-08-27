package graph

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

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
