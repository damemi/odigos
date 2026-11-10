import { gql } from '@apollo/client';

export const CREATE_USER = gql`
  mutation CreateUser($input: WriteUserInput!) {
    createUser(input: $input) {
      generatedPassword
      success
    }
  }
`;

export const UPDATE_USER = gql`
  mutation UpdateUser($userID: String!, $input: WriteUserInput!) {
    updateUser(userID: $userID, input: $input) {
      success
      error
    }
  }
`;

export const RESET_USER_PASSWORD = gql`
  mutation ResetUserPassword($userID: String!) {
    resetUserPassword(userID: $userID) {
      success
      error
      generatedPassword
    }
  }
`;

export const DELETE_USER = gql`
  mutation DeleteUser($userID: String!) {
    deleteUser(userID: $userID) {
      success
      error
    }
  }
`;
