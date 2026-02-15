import { gql } from '@apollo/client';

export const ENSURE_OIDC_IDENTITY_PROVIDER = gql`
  mutation EnsureOidcIdentityProvider($input: OidcIdentityProviderInput!) {
    ensureOidcIdentityProvider(input: $input) {
      success
      error
      callbackUrl
    }
  }
`;

export const ENSURE_SAML_IDENTITY_PROVIDER = gql`
  mutation EnsureSamlIdentityProvider($input: SamlIdentityProviderInput!) {
    ensureSamlIdentityProvider(input: $input) {
      success
      error
      callbackUrl
    }
  }
`;

export const VERIFY_TOKEN = gql`
  mutation VerifyToken($token: String!) {
    verifyToken(token: $token) {
      success
      error
    }
  }
`;

export const SIGN_IN = gql`
  mutation SignIn($email: String, $password: String, $code: String) {
    signIn(email: $email, password: $password, code: $code) {
      idToken
      accessToken
      status
      user {
        id
        email
        username
        role
        needsPasswordChange
        teams {
          id
          name
          description
        }
        computePlatforms {
          id
          name
          type
          status
        }
      }
    }
  }
`;

export const SET_INITIAL_PASSWORD = gql`
  mutation SetInitialPassword($email: String!, $password: String!) {
    setInitialPassword(email: $email, password: $password) {
      idToken
      accessToken
      status
      user {
        id
        email
        username
        role
        needsPasswordChange
      }
    }
  }
`;
