import { AzureIcon, OktaIcon } from '@odigos/ui-kit/icons';
import { AuthProviderId, type AuthProviderOption } from '@/types';
import { LoginType } from '@/types/auth-providers';
export const AUTH_PROVIDERS: AuthProviderOption[] = [
  {
    providerId: AuthProviderId.AZURE,
    displayName: 'Azure',
    icon: AzureIcon,
    discoveryUrl: 'https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration',
    requireTenantId: true,
    isFeatureActive: false,
    loginType: LoginType.OIDC,
  },
  {
    providerId: AuthProviderId.OKTA,
    displayName: 'Okta',
    icon: OktaIcon,
    isFeatureActive: true,
    loginType: LoginType.SAML
  },
];


