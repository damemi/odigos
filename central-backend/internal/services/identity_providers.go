package services

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/central-backend/graph/model"
	"github.com/odigos-io/odigos/central-backend/internal/consts"
	"github.com/odigos-io/odigos/central-backend/internal/keycloak"
)

// IdentityProviderCallbackURL is the redirect URI to register in the identity provider.
func IdentityProviderCallbackURL(providerID model.AuthProviderID) string {
	return fmt.Sprintf("%s/realms/%s/broker/%s/endpoint", consts.GetCentralBackendHost(), consts.KeycloakRealm, strings.ToLower(string(providerID)))
}

// IdentityProviders returns the settings of configured identity providers whose
// alias matches a known provider. Secrets are never returned, only whether one is stored.
func (svc *AdminService) IdentityProviders() ([]*model.IdentityProvider, error) {
	idps, err := svc.ListIdentityProviders()
	if err != nil {
		return nil, err
	}
	return identityProvidersFromRepresentations(idps), nil
}

func identityProvidersFromRepresentations(idps []keycloak.IdentityProviderRepresentation) []*model.IdentityProvider {
	result := make([]*model.IdentityProvider, 0, len(idps))
	seen := make(map[model.AuthProviderID]struct{}, len(idps))
	for _, idp := range idps {
		providerID := model.AuthProviderID(strings.ToUpper(idp.Alias))
		if !providerID.IsValid() {
			continue
		}
		if _, ok := seen[providerID]; ok {
			continue
		}

		provider := &model.IdentityProvider{
			ProviderID:  providerID,
			Enabled:     idp.Enabled,
			CallbackURL: IdentityProviderCallbackURL(providerID),
		}
		switch idp.ProviderID {
		case keycloak.IdentityProviderTypeOIDC:
			provider.Protocol = model.IdentityProviderProtocolOidc
			provider.ClientID = configValue(idp.Config, keycloak.ConfigKeyClientID)
			// Providers set up before the entered values were stored only have the resolved URL.
			provider.DiscoveryURL = configValue(idp.Config, keycloak.ConfigKeyOdigosDiscoveryURL)
			if provider.DiscoveryURL == nil {
				provider.DiscoveryURL = configValue(idp.Config, keycloak.ConfigKeyMetadataDescriptorURL)
			}
			provider.TenantID = configValue(idp.Config, keycloak.ConfigKeyOdigosTenantID)
			provider.HasClientSecret = idp.Config[keycloak.ConfigKeyClientSecret] != ""
		case keycloak.IdentityProviderTypeSAML:
			provider.Protocol = model.IdentityProviderProtocolSaml
			provider.IdpEntityID = configValue(idp.Config, keycloak.ConfigKeyEntityID)
			provider.SingleSignOnServiceURL = configValue(idp.Config, keycloak.ConfigKeySingleSignOnServiceURL)
			provider.HasSigningCertificate = idp.Config[keycloak.ConfigKeySigningCertificate] != ""
		default:
			continue
		}

		seen[providerID] = struct{}{}
		result = append(result, provider)
	}
	return result
}

func configValue(config map[string]string, key string) *string {
	value, ok := config[key]
	if !ok || value == "" {
		return nil
	}
	return &value
}
