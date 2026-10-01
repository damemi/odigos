package distros

import "testing"

func TestEnterpriseGetterIncludesRubyCommunityLegacy(t *testing.T) {
	g, err := NewEnterpriseGetter()
	if err != nil {
		t.Fatal(err)
	}
	d := g.GetDistroByName("ruby-community-legacy")
	if d == nil {
		t.Fatal("ruby-community-legacy missing from enterprise distros")
	}
	if d.FallbackDistro == nil || *d.FallbackDistro != "ruby-community" {
		t.Fatalf("fallbackDistro = %v, want ruby-community", d.FallbackDistro)
	}
	if len(d.RuntimeEnvironments) != 1 || d.RuntimeEnvironments[0].SupportedVersions != ">=2.7,<3.0" {
		t.Fatalf("supportedVersions = %+v, want >=2.7,<3.0", d.RuntimeEnvironments)
	}
	if !d.EnvironmentVariables.OpAmpClientEnvironments {
		t.Fatal("expected OpAMP client environments")
	}
	if !d.ConfigAsEnvVars {
		t.Fatal("expected configAsEnvVars")
	}
	if d.Traces == nil || d.Traces.PayloadCollection == nil || !d.Traces.PayloadCollection.Supported {
		t.Fatal("expected payload collection support")
	}
	if d.Traces.HeadSampling == nil || !d.Traces.HeadSampling.Supported || !d.Traces.HeadSampling.HttpQueryParamsSupported {
		t.Fatal("expected head sampling with HTTP query params")
	}
}
