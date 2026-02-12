import type { SVG } from '@odigos/ui-kit/types';
import { IdentityProviderResult } from './auth';
export enum LoginType {
  OIDC = 'oidc',
  SAML = 'saml',
}

export enum AuthProviderId {
  EMAIL = 'email', // for client-side only
  AZURE = 'AZURE',
  OKTA = 'OKTA',
}

export interface AuthProviderInput {
  providerId: AuthProviderId;
  clientId: string;
  clientSecret: string;
  discoveryUrl: string;
  tenantId: string;
}

export type GetAuthSignInUrl = (providerId: AuthProviderId) => Promise<string | undefined>;
export type EnsureIdentityProvider = (input: AuthProviderInput) => Promise<IdentityProviderResult | undefined>;
export type EnsureSamlIdentityProvider = (input: SamlIdentityProviderInput) => Promise<IdentityProviderResult | undefined>;
export interface SamlIdentityProviderInput {
  providerId: AuthProviderId;
  idpEntityId: string;
  singleSignOnServiceUrl: string;
  signingCertificate: string;
}

export interface AuthProviderOption {
  providerId: AuthProviderId;
  displayName: string;
  icon: SVG;
  discoveryUrl?: string;
  requireTenantId?: boolean;
  isFeatureActive?: boolean;
  loginType?: LoginType;
}

