import { gql } from '@apollo/client';

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
