import { gql } from '@apollo/client';

export const REMOTE_FETCH = gql`
  query RemoteFetch($proxyID: String!, $query: String!, $variables: JSON!) {
    remoteFetch(proxyID: $proxyID, query: $query, variables: $variables)
  }
`;
