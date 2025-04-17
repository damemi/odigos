import { gql } from '@apollo/client'

export const GET_CONNECTIONS = gql`
  query GetConnections {
    connections {
      proxyID
      status
      name
    }
  }
`
