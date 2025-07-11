import { gql } from '@apollo/client';

export const GET_USERS = gql`
  query GetUsers {
    users {
      id
      email
      username
      role
      needsPasswordChange
      teams {
        id
        name
      }
      computePlatforms {
        id
        name
      }
    }
  }
`;
