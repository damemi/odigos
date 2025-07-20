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

export const DELETE_USER = gql`
  mutation DeleteUser($userID: String!) {
    deleteUser(userID: $userID) {
      success
      error
    }
  }
`;
