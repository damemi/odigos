import { gql } from '@apollo/client';

export const CREATE_USER = gql`
  mutation CreateUser($email: String!, $password: String, $role: Role!) {
    createUser(email: $email, password: $password, role: $role) {
      success
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
