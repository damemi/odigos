package services

import (
	"testing"

	"github.com/odigos-io/central-backend/graph/model"
)

func TestOidcTenantIDRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		providerID   model.AuthProviderID
		discoveryURL string
		want         bool
	}{
		{
			name:         "google static discovery",
			providerID:   model.AuthProviderIDGoogle,
			discoveryURL: "https://accounts.google.com/.well-known/openid-configuration",
			want:         false,
		},
		{
			name:         "okta templated discovery",
			providerID:   model.AuthProviderIDOkta,
			discoveryURL: "https://{tenantid}.okta.com/.well-known/openid-configuration",
			want:         true,
		},
		{
			name:         "azure organizations discovery still needs tenant for issuer",
			providerID:   model.AuthProviderIDAzure,
			discoveryURL: "https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration",
			want:         true,
		},
		{
			name:         "custom discovery with placeholder",
			providerID:   model.AuthProviderIDGoogle,
			discoveryURL: "https://example.com/{tenantid}/.well-known/openid-configuration",
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := oidcTenantIDRequired(tt.providerID, tt.discoveryURL); got != tt.want {
				t.Fatalf("oidcTenantIDRequired(%q, %q) = %v, want %v", tt.providerID, tt.discoveryURL, got, tt.want)
			}
		})
	}
}
