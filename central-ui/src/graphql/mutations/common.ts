import { gql } from '@apollo/client';

export const REMOTE_FETCH = gql`
  query RemoteFetch($proxyID: String!, $query: String!, $variables: JSON!) {
    remoteFetch(proxyID: $proxyID, query: $query, variables: $variables)
  }
`;

export const REMOTE_MULTI_FETCH = gql`
  query RemoteMultiFetch($proxyIDs: [String!]!, $query: String!, $variables: JSON!) {
    remoteMultiFetch(proxyIDs: $proxyIDs, query: $query, variables: $variables) {
      proxyID
      success
      data
      error
    }
  }
`;
