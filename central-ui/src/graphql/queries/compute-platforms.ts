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
      collectors {
        type
        management
        deployment
        mode
      }
      connectedAt
      lastSeenAt
      stats {
        workloads
        instrumentedWorkloads
        workloadsWithIssues
        workloadsInProgress
        namespaces
        autoInstrumentedNamespaces
        updatedAt
      }
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
