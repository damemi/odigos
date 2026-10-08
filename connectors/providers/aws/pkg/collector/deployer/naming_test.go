package deployer

import (
	"strings"
	"testing"
)

func TestNamesMatchTheDocumentedSpellings(t *testing.T) {
	if got, want := LogGroupName("prod"), "/odigos/collector/prod"; got != want {
		t.Errorf("log group = %q, want %q", got, want)
	}
	if got, want := CredentialSecretName("prod", "eu-west-1"), "odigos/collector/prod/eu-west-1/credential"; got != want {
		t.Errorf("credential secret = %q, want %q", got, want)
	}
	if got, want := LicenseSecretName("prod", "eu-west-1"), "odigos/collector/prod/eu-west-1/license"; got != want {
		t.Errorf("licence secret = %q, want %q", got, want)
	}
}

// TestTheCredentialSecretIsPerRegionalCollector. Revocation is per Collector: whoever holds one
// can read that Collector's desired configuration. A host-network move must not create a second regional
// Collector or secret, while two regions remain distinct.
func TestTheCredentialSecretIsPerCollector(t *testing.T) {
	a := CredentialSecretName("prod", "eu-west-1")
	b := CredentialSecretName("prod", "eu-west-1")
	sameIDOtherRegion := CredentialSecretName("prod", "us-east-1")
	if a != b || a == sameIDOtherRegion {
		t.Fatalf("credential secret names do not have regional identity: %q / %q / %q", a, b, sameIDOtherRegion)
	}
	// The licence secret shares the region path with the credential secret, so its leaf must remain
	// distinct.
	if strings.HasPrefix(licenseSecretLeaf, "vpc-") {
		t.Errorf("the licence secret leaf %q has a VPC id's shape, so a network could collide with it", licenseSecretLeaf)
	}
}
