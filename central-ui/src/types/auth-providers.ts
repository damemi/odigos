import type { SVG } from '@odigos/ui-kit/types';
import { IdentityProviderResult } from './auth';

export enum LoginType {
  OIDC = 'oidc',
  SAML = 'saml',
}

export enum AuthProviderId {
  EMAIL = 'email', // for client-side only
  // GOOGLE = 'GOOGLE',
  AZURE = 'AZURE',
  OKTA = 'OKTA',
}

export interface OidcIdentityProviderInput {
  providerId: AuthProviderId;
  clientId: string;
  clientSecret: string;
  discoveryUrl: string;
  tenantId: string;
}

export interface SamlIdentityProviderInput {
  providerId: AuthProviderId;
  idpEntityId: string;
  singleSignOnServiceUrl: string;
  signingCertificate: string;
}

export type GetAuthSignInUrl = (providerId: AuthProviderId) => Promise<string | undefined>;
export type EnsureOidcIdentityProvider = (input: OidcIdentityProviderInput) => Promise<IdentityProviderResult | undefined>;
export type EnsureSamlIdentityProvider = (input: SamlIdentityProviderInput) => Promise<IdentityProviderResult | undefined>;

export interface AuthProviderOption {
  providerId: AuthProviderId;
  displayName: string;
  icon: SVG;
  discoveryUrl?: string;
  defaultLoginType?: LoginType;
}

export enum IdentityProviderProtocol {
  OIDC = 'OIDC',
  SAML = 'SAML',
}

// Settings of a configured identity provider. Secrets are never returned, only whether one is stored.
export interface IdentityProvider {
  providerId: AuthProviderId;
  protocol: IdentityProviderProtocol;
  enabled: boolean;
  callbackUrl: string;
  clientId?: string | null;
  discoveryUrl?: string | null;
  tenantId?: string | null;
  hasClientSecret: boolean;
  idpEntityId?: string | null;
  singleSignOnServiceUrl?: string | null;
  hasSigningCertificate: boolean;
}
