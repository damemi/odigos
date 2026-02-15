import { LoginType } from '@/types/auth-providers';
import { AzureIcon, OktaIcon } from '@odigos/ui-kit/icons';
import { AuthProviderId, type AuthProviderOption } from '@/types';

export const AUTH_PROVIDERS: AuthProviderOption[] = [
  {
    providerId: AuthProviderId.AZURE,
    displayName: 'Azure',
    icon: AzureIcon,
    discoveryUrl: 'https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration',
    requireTenantId: true,
    loginType: LoginType.OIDC,
  },
  {
    providerId: AuthProviderId.OKTA,
    displayName: 'Okta',
    icon: OktaIcon,
    loginType: LoginType.SAML,
  },
];
