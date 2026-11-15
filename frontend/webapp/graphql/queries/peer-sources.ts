import { gql } from '@apollo/client';

export const GET_PEER_SOURCES = gql`
  query GetPeerSources($serviceName: String!, $namespace: String) {
    peerSources(serviceName: $serviceName, namespace: $namespace) {
      inbound {
        nodeId
        serviceName
        isVirtual
        requests
        dateTime
        nodeAttributes {
          key
          value
        }
      }
      outbound {
        nodeId
        serviceName
        isVirtual
        requests
        dateTime
        nodeAttributes {
          key
          value
        }
      }
    }
  }
`;
