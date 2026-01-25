import { gql } from '@apollo/client';

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
