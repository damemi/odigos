import { AzureIcon } from '@odigos/ui-kit/icons';
import { OidcProviderId, type OidcProviderOption } from '@/types';

export const OIDC_PROVIDERS: OidcProviderOption[] = [
  {
    providerId: OidcProviderId.AZURE,
    displayName: 'Azure',
    icon: AzureIcon,
    discoveryUrl: 'https://login.microsoftonline.com/organizations/v2.0/.well-known/openid-configuration',
    requireTenantId: true,
  },
];
