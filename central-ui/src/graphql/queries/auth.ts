import { gql } from '@apollo/client';

export const GET_OIDC_SIGN_IN_URL = gql`
  query GetOidcSignInUrl($providerId: OidcProviderId!) {
    oidcSignInUrl(providerId: $providerId)
  }
`;

export const GET_SIGN_OUT_URL = gql`
  query GetSignOutUrl($idToken: String) {
    signOutUrl(idToken: $idToken)
  }
`;
