import { gql } from '@apollo/client';

export const GET_COMPUTE_PLATFORMS = gql`
  query GetComputePlatforms {
    computePlatforms {
      id
      name
      odigosVersion
      graphqlSchemaVersion
      type
      status
      connectedAt
      lastSeenAt
      users {
        id
        email
        username
        role
      }
      teams {
        id
        name
      }
    }
  }
`;
