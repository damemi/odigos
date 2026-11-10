import { gql } from '@apollo/client';

export const GET_ALL_CLUSTER_SNAPSHOTS = gql`
  query GetAllClusterSnapshots {
    allClusterSnapshots {
      clusters {
        clusterId
        clusterName
        namespaces {
          name
          selected
          workloads {
            namespace
            kind
            name
            selected
            disabled
            otelServiceName
            healthStatus
          }
        }
        timestamp
      }
      totalWorkloads
      totalNamespaces
      lastUpdated
    }
  }
`;

export const GET_CLUSTER_SNAPSHOT = gql`
  query GetClusterSnapshot($clusterId: String!) {
    clusterSnapshot(clusterId: $clusterId) {
      clusterId
      clusterName
      namespaces {
        name
        selected
        workloads {
          namespace
          kind
          name
          selected
          disabled
          otelServiceName
          healthStatus
        }
      }
      timestamp
    }
  }
`;
