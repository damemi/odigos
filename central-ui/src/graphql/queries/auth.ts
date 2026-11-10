import { gql } from '@apollo/client';

export const GET_CONFIGURED_IDENTITY_PROVIDERS = gql`
  query ConfiguredIdentityProviders {
    configuredIdentityProviders
  }
`;

export const SIGN_IN_URL = gql`
  query signInUrl($providerId: AuthProviderId!) {
    signInUrl(providerId: $providerId)
  }
`;

export const GET_SIGN_OUT_URL = gql`
  query GetSignOutUrl($idToken: String) {
    signOutUrl(idToken: $idToken)
  }
`;

export const GET_IDENTITY_PROVIDERS = gql`
  query IdentityProviders {
    identityProviders {
      providerId
      protocol
      enabled
      callbackUrl
      clientId
      discoveryUrl
      tenantId
      hasClientSecret
      idpEntityId
      singleSignOnServiceUrl
      hasSigningCertificate
    }
  }
`;
