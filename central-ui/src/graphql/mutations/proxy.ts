import { gql } from '@apollo/client';

export const DELETE_PROXY_CONNECTION = gql`
  mutation DeleteProxyConnection($proxyID: String!) {
    deleteProxyConnection(proxyID: $proxyID) {
      success
      error
    }
  }
`;
