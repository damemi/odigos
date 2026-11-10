package collectorimage

import (
	"strings"
	"testing"
)

const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func goodPin() Pin {
	return Pin{
		Digest: digest,
		Tag:    "v1.36.0",
		Repositories: map[Cloud]string{
			CloudAWS:   "ghcr.io/odigos-io/odigos-enterprise-collector",
			CloudAzure: "ghcr.io/odigos-io/odigos-enterprise-collector",
			CloudGCP:   "us-central1-docker.pkg.dev/odigos-cloud/components/odigos-enterprise-collector",
		},
		Platforms: []Arch{ArchAMD64, ArchARM64},
	}
}

// TestResolveIsAlwaysDigestPinned: a tag is mutable, so two deployments of "the same" tagged image are
// not the same bytes and a drift check against a tag can only ever say "the tag matches" — which is
// exactly the check that passes while the image underneath has changed.
func TestResolveIsAlwaysDigestPinned(t *testing.T) {
	p := goodPin()
	for _, c := range []Cloud{CloudAWS, CloudAzure, CloudGCP} {
		res, err := Resolve(p, c, ArchAMD64, Override{})
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if !strings.HasSuffix(res.Ref, "@"+digest) {
			t.Errorf("%s: ref = %q, want a digest-pinned reference", c, res.Ref)
		}
		if strings.Contains(res.Ref, p.Tag) {
			t.Errorf("%s: ref = %q carries the tag; a runtime must resolve the digest", c, res.Ref)
		}
	}
}

// TestOneDigestAcrossEveryCloud is the shared image contract: the registry host differs because that is
// what each cloud can pull from, and the bytes do not. A customer applying an artifact that pulls from
// GHCR and a Collector the deployer created from Artifact Registry must be provably identical.
func TestOneDigestAcrossEveryCloud(t *testing.T) {
	p := goodPin()
	seen := map[string]bool{}
	for _, c := range []Cloud{CloudAWS, CloudAzure, CloudGCP} {
		res, err := Resolve(p, c, ArchAMD64, Override{})
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		at := strings.LastIndex(res.Ref, "@")
		seen[res.Ref[at+1:]] = true
	}
	if len(seen) != 1 {
		t.Errorf("clouds resolved to %d distinct digests, want exactly one", len(seen))
	}
}

// TestArchitectureSupportIsExplicit: both architectures resolve, an architecture the manifest list does
// not carry is REFUSED rather than attempted, and the refusal says which ones exist. Attempting it
// instead produces a task that will not start with an error that reads as a capacity problem.
func TestArchitectureSupportIsExplicit(t *testing.T) {
	p := goodPin()
	for _, a := range []Arch{ArchAMD64, ArchARM64} {
		if !p.Supports(a) {
			t.Errorf("the released pin does not carry %s", a)
		}
		if _, err := Resolve(p, CloudAWS, a, Override{}); err != nil {
			t.Errorf("%s refused: %v", a, err)
		}
	}

	amd64Only := goodPin()
	amd64Only.Platforms = []Arch{ArchAMD64}
	_, err := Resolve(amd64Only, CloudAWS, ArchARM64, Override{})
	if err == nil {
		t.Fatal("arm64 resolved against an amd64-only manifest list")
	}
	for _, want := range []string{"arm64", "amd64", "collector.regions"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// TestDefaultArch: unset means DefaultArch, and DefaultArch must be one the pin carries or every network
// that names no architecture fails.
func TestDefaultArch(t *testing.T) {
	p := goodPin()
	unset, err := Resolve(p, CloudAWS, "", Override{})
	if err != nil {
		t.Fatalf("an unset architecture failed: %v", err)
	}
	explicit, err := Resolve(p, CloudAWS, DefaultArch, Override{})
	if err != nil || unset != explicit {
		t.Errorf("unset = %q, %s = %q", unset, DefaultArch, explicit)
	}
	if !p.Supports(DefaultArch) {
		t.Errorf("the released pin does not carry the default architecture %s", DefaultArch)
	}
}

// TestValidateCatchesMalformedPins runs in a test rather than at startup on purpose: a connector that
// discovers its own image pin is invalid at deploy time has already told a customer their Collector is
// broken.
func TestValidateCatchesMalformedPins(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Pin)
		want string
	}{
		{"no version", func(p *Pin) { p.Digest = ""; p.Tag = "" }, "pins nothing"},
		{"short digest", func(p *Pin) { p.Digest = "sha256:1111" }, "not a full sha256"},
		{"tag instead of digest", func(p *Pin) { p.Digest = "v1.36.0" }, "not a full sha256"},
		{"invalid tag", func(p *Pin) { p.Tag = "bad tag" }, "invalid tag"},
		{"no platforms", func(p *Pin) { p.Platforms = nil }, "no platforms"},
		{"unknown platform", func(p *Pin) { p.Platforms = []Arch{"s390x"} }, "unknown platform"},
		{"missing a cloud", func(p *Pin) { delete(p.Repositories, CloudAzure) }, "no repository for azure"},
		{"repository carries a tag", func(p *Pin) { p.Repositories[CloudAWS] += ":v1" }, "carries a tag"},
		{
			"gcp pointed at a registry Cloud Run cannot pull",
			func(p *Pin) { p.Repositories[CloudGCP] = "registry.odigos.io/odigos-enterprise-collector" },
			"Cloud Run can pull",
		},
	}
	for _, tc := range cases {
		p := goodPin()
		tc.mut(&p)
		problems := p.Validate()
		if len(problems) == 0 {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		var found bool
		for _, pr := range problems {
			if strings.Contains(pr, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: problems = %v, want one mentioning %q", tc.name, problems, tc.want)
		}
	}

	if problems := goodPin().Validate(); len(problems) != 0 {
		t.Errorf("a well-formed pin was rejected: %v", problems)
	}
}

// TestCloudRunPullableSources encodes the constraint that forced GCP's ref to differ: Cloud Run accepts
// Artifact Registry, public Docker Hub and public GHCR, and has no credential mechanism for anything
// else — so an arbitrary registry host is not a mirror decision, it simply cannot be pulled.
func TestCloudRunPullableSources(t *testing.T) {
	pullable := []string{
		"us-central1-docker.pkg.dev/odigos-cloud/components/odigos-enterprise-collector",
		"europe-west1-docker.pkg.dev/p/r/i",
		"ghcr.io/odigos-io/odigos-enterprise-collector",
		"keyval/odigos-enterprise-collector",
		"docker.io/keyval/odigos-enterprise-collector",
	}
	for _, repo := range pullable {
		if !cloudRunPullable(repo) {
			t.Errorf("%q reported as not pullable by Cloud Run", repo)
		}
	}
	for _, repo := range []string{
		"registry.odigos.io/odigos-enterprise-collector",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/collector",
		"myregistry.azurecr.io/collector",
	} {
		if cloudRunPullable(repo) {
			t.Errorf("%q reported as pullable by Cloud Run", repo)
		}
	}
}

func TestConfiguredPinIsRunnable(t *testing.T) {
	configured, err := FromReference("us-central1-docker.pkg.dev/odigos-cloud/components/collector@" + digest)
	if err != nil {
		t.Fatal(err)
	}
	if problems := configured.Validate(); len(problems) != 0 {
		t.Fatalf("the configured pin is invalid: %v", problems)
	}
	for _, cloud := range []Cloud{CloudAWS, CloudAzure, CloudGCP} {
		for _, arch := range []Arch{ArchAMD64, ArchARM64} {
			res, err := Resolve(configured, cloud, arch, Override{})
			if err != nil {
				t.Errorf("Resolve(%s, %s): %v", cloud, arch, err)
			} else if !strings.HasSuffix(res.Ref, "@"+configured.Digest) {
				t.Errorf("Resolve(%s, %s) = %q, want the released digest", cloud, arch, res.Ref)
			}
		}
	}
	if repo := configured.Repositories[CloudGCP]; !cloudRunPullable(repo) {
		t.Errorf("the released GCP repository %q is not pullable by Cloud Run", repo)
	}
}

// TestPullSecretRefIsAlwaysCarriable: the image is not going to stay public forever, so a deployment must
// be able to attach a pull credential on every cloud, not only once a customer also overrides the
// registry.
func TestPullSecretRefIsAlwaysCarriable(t *testing.T) {
	p := goodPin()
	for _, c := range []Cloud{CloudAWS, CloudAzure, CloudGCP} {
		res, err := Resolve(p, c, ArchAMD64, Override{PullSecretRef: "projects/o/secrets/collector-pull"})
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if res.PullSecretRef != "projects/o/secrets/collector-pull" {
			t.Errorf("%s: PullSecretRef = %q, want it carried through unchanged", c, res.PullSecretRef)
		}
		if !strings.HasSuffix(res.Ref, "@"+digest) {
			t.Errorf("%s: ref = %q, want a digest-pinned reference even with a pull credential set", c, res.Ref)
		}
	}
}

// TestRegistryOverrideReplacesTheCompiledRepository: a customer running their own mirror gets the same
// digest pinned against their registry rather than Odigos's.
func TestRegistryOverrideReplacesTheCompiledRepository(t *testing.T) {
	p := goodPin()
	res, err := Resolve(p, CloudAWS, ArchAMD64, Override{Registry: "mirror.customer.example/odigos-enterprise-collector"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	want := "mirror.customer.example/odigos-enterprise-collector@" + digest
	if res.Ref != want {
		t.Errorf("ref = %q, want %q", res.Ref, want)
	}
}

// TestMalformedRegistryOverrideIsRefused: an override carrying a tag or digest of its own would silently
// stop being digest-pinned once Resolve appends its own digest.
func TestMalformedRegistryOverrideIsRefused(t *testing.T) {
	p := goodPin()
	_, err := Resolve(p, CloudAWS, ArchAMD64, Override{Registry: "mirror.customer.example/collector:v1"})
	if err == nil {
		t.Fatal("a registry override carrying a tag was accepted")
	}
	if !strings.Contains(err.Error(), "carries a tag") {
		t.Errorf("refusal does not explain why: %v", err)
	}
}

// TestGCPRegistryOverrideStillRespectsCloudRun: Cloud Run's pull constraint is about which sources it can
// resolve at all, not about authentication — a pull credential does not let it pull an arbitrary registry
// a customer's mirror might use.
func TestGCPRegistryOverrideStillRespectsCloudRun(t *testing.T) {
	p := goodPin()
	_, err := Resolve(p, CloudGCP, ArchAMD64, Override{
		Registry:      "myregistry.azurecr.io/collector",
		PullSecretRef: "projects/o/secrets/collector-pull",
	})
	if err == nil {
		t.Fatal("a gcp registry override Cloud Run cannot pull from was accepted, even with a pull credential")
	}
	if !strings.Contains(err.Error(), "Cloud Run can pull") {
		t.Errorf("refusal does not explain why: %v", err)
	}
}

// TestDescribeNamesTheBuildWithoutBeingAReference: a status message needs something a human can compare
// between two Collectors, and a bare tag is not it.
func TestDescribeNamesTheBuildWithoutBeingAReference(t *testing.T) {
	got := Describe(goodPin(), CloudAWS)
	if !strings.Contains(got, "v1.36.0") {
		t.Errorf("Describe = %q, want the tag a human reads", got)
	}
	if !strings.Contains(got, "sha256:") {
		t.Errorf("Describe = %q, want enough digest to tell two builds apart", got)
	}
	if strings.Contains(got, digest) {
		t.Errorf("Describe = %q, want a shortened digest rather than the whole reference", got)
	}
}
