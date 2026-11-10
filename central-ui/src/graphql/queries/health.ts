import { gql } from '@apollo/client';

export const GET_HEALTH = gql`
  query GetHealth {
    health {
      backend {
        redisStatus {
          status
          message
        }
        keycloakStatus {
          status
          message
        }
        sseStatus {
          status
          message
        }
      }
    }
  }
`;
