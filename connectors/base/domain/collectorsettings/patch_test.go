package collectorsettings

import (
	"testing"
)

func s(v string) *string { return &v }
func i(v int) *int       { return &v }
func b(v bool) *bool     { return &v }

func TestValidateRejectsUnusableValues(t *testing.T) {
	cases := []struct {
		name  string
		patch Patch
	}{
		{"non-numeric cpu", Patch{CPU: s("large")}},
		// Memory also sizes the memory_limiter, so an unparseable value is not merely a deploy problem.
		{"non-numeric memory", Patch{Memory: s("2Gi")}},
		{"negative log retention", Patch{LogRetentionDays: i(-1)}},
		{"negative rollout batch", Patch{RolloutBatchSize: i(-5)}},
		{"unknown arch", Patch{Arch: s("riscv")}},
		{"unknown scaling mode", Patch{Scaling: &ScalingPatch{Mode: "elastic"}}},
		{"min above max", Patch{Scaling: &ScalingPatch{Mode: ScalingManual, MinCount: i(5), MaxCount: i(2)}}},
		{"target percent out of range", Patch{Scaling: &ScalingPatch{Mode: ScalingAuto, TargetCPUPercent: i(150)}}},
		{"unknown tls mode", Patch{TLS: &TransportPatch{Mode: s("mtls")}}},
		// A load balancer's generated DNS name is not issuable by any CA, so a certificate mode without
		// a hostname the customer owns fails verification on every export.
		{"certificate mode without hostname", Patch{TLS: &TransportPatch{Mode: s("loadBalancer")}}},
		{"negative fallback prefix", Patch{Ingress: &IngressPatch{MinFallbackPrefixLength: i(-1)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.patch.Validate(); err == nil {
				t.Fatal("accepted a value the deployer could not use")
			}
		})
	}
}

func TestValidateAcceptsAWorkableSet(t *testing.T) {
	p := Patch{
		CPU: s("1024"), Memory: s("2048"), LogRetentionDays: i(7), Arch: s("arm64"),
		Scaling: &ScalingPatch{Mode: ScalingAuto, MinCount: i(2), MaxCount: i(6), TargetCPUPercent: i(70)},
		TLS:     &TransportPatch{Mode: s("loadBalancer"), Hostname: s("otel.example.com")},
		Ingress: &IngressPatch{MinFallbackPrefixLength: i(20), AllowNetworkCIDRFallback: b(false)},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// An omitted field must leave what is there alone — the whole contract of a partial update.
func TestMergeIntoKeepsUntouchedFields(t *testing.T) {
	existing := map[string]any{KeyCPU: "2048", KeyHostNetworkID: "vpc-1"}
	got := Patch{Memory: s("4096")}.MergeInto(existing)

	if got[KeyCPU] != "2048" {
		t.Errorf("cpu = %v, want the existing 2048", got[KeyCPU])
	}
	if got[KeyHostNetworkID] != "vpc-1" {
		t.Errorf("hostNetworkId = %v, want the existing vpc-1", got[KeyHostNetworkID])
	}
	if got[KeyMemory] != "4096" {
		t.Errorf("memory = %v, want the patched 4096", got[KeyMemory])
	}
	// The caller's map is a view of the live effective config; writing through it would change what a
	// concurrent reader sees before the write is even persisted.
	if _, mutated := existing[KeyMemory]; mutated {
		t.Error("MergeInto mutated the entry it was given")
	}
}

// An explicitly empty list is an instruction — "none of the inherited defaults" — and must reach the
// tree as an empty list rather than being skipped as if unset.
func TestMergeIntoWritesAnExplicitlyEmptyList(t *testing.T) {
	got := Patch{Provider: map[string]any{"subnetIds": []any{}}}.MergeInto(map[string]any{
		KeyProvider: map[string]any{"subnetIds": []any{"subnet-1"}},
	})

	provider, _ := got[KeyProvider].(map[string]any)
	list, ok := provider["subnetIds"].([]any)
	if !ok {
		t.Fatalf("subnetIds = %T, want a list", provider["subnetIds"])
	}
	if len(list) != 0 {
		t.Errorf("subnetIds = %v, want an empty list", list)
	}
}

func TestMergeIntoNestsScalingTLSAndIngress(t *testing.T) {
	got := Patch{
		Scaling: &ScalingPatch{Mode: ScalingManual, DesiredCount: i(3)},
		TLS:     &TransportPatch{Mode: s("collector"), Hostname: s("otel.example.com")},
		Ingress: &IngressPatch{AllowNetworkCIDRFallback: b(false)},
	}.MergeInto(nil)

	scaling, _ := got[KeyScaling].(map[string]any)
	if scaling[KeyScalingMode] != ScalingManual || scaling[KeyDesiredCount] != 3 {
		t.Errorf("scaling = %v", scaling)
	}
	tls, _ := got[KeyTLS].(map[string]any)
	if tls[KeyTLSMode] != "collector" {
		t.Errorf("tls = %v", tls)
	}
	ingress, _ := got[KeyIngress].(map[string]any)
	if ingress[KeyAllowNetworkCIDRFallback] != false {
		t.Errorf("ingress = %v", ingress)
	}
}

func TestMergeIntoNestsImageRepositoryOverride(t *testing.T) {
	got := Patch{ImageRepositoryOverride: s("registry.example.com/odigos")}.MergeInto(nil)
	image, _ := got[KeyImage].(map[string]any)
	if image[KeyRepoOverride] != "registry.example.com/odigos" {
		t.Errorf("image = %v", image)
	}
}

// Writing an enabled region turns the feature switch on: creating a Collector through the API and
// having nothing happen — because a top-level flag nobody mentioned is still false — is not a
// defensible outcome of an explicit create.
func TestWriteRegionEnablesTheFeatureSwitch(t *testing.T) {
	tree, err := WriteRegion(map[string]any{KeyEnabled: false}, "eu-west-1", Patch{Enabled: b(true)})
	if err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}
	if tree[KeyEnabled] != true {
		t.Error("the feature switch stayed off after an enabled region was written")
	}

	settings := Read(map[string]any{Key: tree})
	if !settings.Region("eu-west-1").Deploys() {
		t.Error("the written region does not deploy")
	}
}

// A region written as disabled must not flip the switch on for every other region.
func TestWriteRegionDisabledLeavesTheSwitchAlone(t *testing.T) {
	tree, err := WriteRegion(map[string]any{KeyEnabled: false}, "eu-west-1", Patch{Enabled: b(false)})
	if err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}
	if tree[KeyEnabled] == true {
		t.Error("a disabled region turned the feature switch on")
	}
}

func TestWriteRegionValidatesAndRefusesABlankRegion(t *testing.T) {
	if _, err := WriteRegion(nil, "", Patch{}); err == nil {
		t.Fatal("a blank region was accepted")
	}
	if _, err := WriteRegion(nil, "eu-west-1", Patch{CPU: s("huge")}); err == nil {
		t.Fatal("WriteRegion skipped validation")
	}
}

func TestWriteRegionDoesNotMutateTheTreeItIsGiven(t *testing.T) {
	original := map[string]any{KeyRegions: map[string]any{"us-east-1": map[string]any{KeyCPU: "512"}}}
	if _, err := WriteRegion(original, "eu-west-1", Patch{CPU: s("1024")}); err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}
	regions, _ := original[KeyRegions].(map[string]any)
	if _, leaked := regions["eu-west-1"]; leaked {
		t.Error("WriteRegion wrote into the caller's tree")
	}
}

// Removal rather than a disabled entry: a leftover would be silently re-applied, with whatever sizes
// and overrides it carried, to a Collector created in that region months later.
func TestRemoveRegionDropsTheEntry(t *testing.T) {
	tree, err := WriteRegion(nil, "eu-west-1", Patch{Enabled: b(true), CPU: s("1024")})
	if err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}
	if !HasRegion(tree, "eu-west-1") {
		t.Fatal("the region was not written")
	}

	after := RemoveRegion(tree, "eu-west-1")
	if HasRegion(after, "eu-west-1") {
		t.Error("the region survived removal")
	}
	if !HasRegion(tree, "eu-west-1") {
		t.Error("RemoveRegion mutated the tree it was given")
	}
}

// HasRegion answers "did somebody configure this", which is what separates a create from an update —
// not "can settings be resolved for it", which is true of every region.
func TestHasRegionIsAboutExplicitConfiguration(t *testing.T) {
	tree, err := WriteRegion(nil, "eu-west-1", Patch{Enabled: b(true)})
	if err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}
	if HasRegion(tree, "us-east-1") {
		t.Error("an unconfigured region reported as configured")
	}

	settings := Read(map[string]any{Key: tree})
	if settings.Region("us-east-1").Region != "us-east-1" {
		t.Error("an unconfigured region should still resolve from the defaults")
	}
}

func TestConfiguredRegionsIsStable(t *testing.T) {
	tree := map[string]any{}
	for _, region := range []string{"us-east-1", "ap-south-1", "eu-west-1"} {
		var err error
		if tree, err = WriteRegion(tree, region, Patch{Enabled: b(true)}); err != nil {
			t.Fatalf("WriteRegion %s: %v", region, err)
		}
	}
	got := ConfiguredRegions(tree)
	want := []string{"ap-south-1", "eu-west-1", "us-east-1"}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("ConfiguredRegions = %v, want %v", got, want)
		}
	}
}

// The round trip that matters: what a mutation writes is what the reconcile loop reads back.
func TestWrittenSettingsResolveBack(t *testing.T) {
	tree, err := WriteRegion(nil, "eu-west-1", Patch{
		Enabled: b(true), HostNetworkID: s("vpc-42"), CPU: s("2048"), Memory: s("4096"),
		Scaling: &ScalingPatch{Mode: ScalingAuto, MinCount: i(2), MaxCount: i(8)},
		Ingress: &IngressPatch{AdditionalCIDRs: &[]string{"10.1.0.0/16"}},
	})
	if err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}

	region := Read(map[string]any{Key: tree}).Region("eu-west-1")
	if region.HostNetworkID != "vpc-42" {
		t.Errorf("hostNetworkId = %q", region.HostNetworkID)
	}
	if region.CPU != "2048" || region.Memory != "4096" {
		t.Errorf("size = (%q, %q)", region.CPU, region.Memory)
	}
	if region.Scaling.Mode != ScalingAuto || region.Scaling.MaxCount != 8 {
		t.Errorf("scaling = %+v", region.Scaling)
	}
	if len(region.Ingress.AdditionalCIDRs) != 1 || region.Ingress.AdditionalCIDRs[0] != "10.1.0.0/16" {
		t.Errorf("ingress cidrs = %v", region.Ingress.AdditionalCIDRs)
	}
}
