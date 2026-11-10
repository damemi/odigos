import { gql } from '@apollo/client';

export const GET_TEAMS = gql`
  query GetTeams {
    teams {
      id
      name
      description
      users {
        id
        email
        username
        role
      }
      computePlatforms {
        id
        name
      }
    }
  }
`;
