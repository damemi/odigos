import { gql } from '@apollo/client';

export const VERIFY_TOKEN = gql`
  mutation VerifyToken($token: String!) {
    verifyToken(token: $token) {
      success
      error
    }
  }
`;

export const SIGN_IN = gql`
  mutation SignIn($email: String!, $password: String!) {
    signIn(email: $email, password: $password) {
      token
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
      status
    }
  }
`;

export const SET_INITIAL_PASSWORD = gql`
  mutation SetInitialPassword($email: String!, $password: String!) {
    setInitialPassword(email: $email, password: $password) {
      token
      user {
        id
        email
        username
        role
        needsPasswordChange
      }
      status
    }
  }
`;

export const SIGN_OUT = gql`
  mutation SignOut {
    signOut {
      success
      error
    }
  }
`;
