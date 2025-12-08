import type { SVG } from '@odigos/ui-kit/types';

export enum OidcProviderId {
  EMAIL = 'email', // for client-side only
  AZURE = 'azure',
}

export interface OidcProviderInput {
  providerId: OidcProviderId;
  clientId: string;
  clientSecret: string;
  discoveryUrl: string;
  tenantId: string;
}

export interface OidcProviderOption {
  providerId: OidcProviderId;
  displayName: string;
  icon: SVG;
  discoveryUrl?: string;
  requireTenantId?: boolean;
}
