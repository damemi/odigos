import { LoginType } from '@/types/auth-providers';
import { AzureIcon, GoogleCloudPlatformLogo, OktaIcon } from '@odigos/ui-kit/icons';
import { AuthProviderId, type AuthProviderOption } from '@/types';

// defaultLoginType is the default protocol shown during first-time IdP setup.
// Users can toggle between OIDC and SAML via Segment on the setup form.
// discoveryUrl may include a {tenantid} placeholder resolved from Tenant ID.
export const AUTH_PROVIDERS: AuthProviderOption[] = [
  {
    providerId: AuthProviderId.GOOGLE,
    displayName: 'Google',
    icon: GoogleCloudPlatformLogo,
    discoveryUrl: 'https://accounts.google.com/.well-known/openid-configuration',
    defaultLoginType: LoginType.OIDC,
  },
  {
    providerId: AuthProviderId.AZURE,
    displayName: 'Azure',
    icon: AzureIcon,
    discoveryUrl: 'https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration',
    defaultLoginType: LoginType.OIDC,
  },
  {
    providerId: AuthProviderId.OKTA,
    displayName: 'Okta',
    icon: OktaIcon,
    discoveryUrl: 'https://{tenantid}.okta.com/.well-known/openid-configuration',
    defaultLoginType: LoginType.SAML,
  },
];

// Tenant ID is required when the discovery URL templates {tenantid}, or for Azure
// (multi-tenant discovery returns an issuer that still contains {tenantid}).
export const isOidcTenantIdRequired = (discoveryUrl: string, providerId: AuthProviderId): boolean => {
  if (discoveryUrl.includes('{tenantid}')) {
    return true;
  }
  return providerId === AuthProviderId.AZURE;
};
