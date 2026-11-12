import { gql } from '@apollo/client';

export const GET_SYSTEM_CONFIG = gql`
  query GetSystemConfig {
    systemConfig {
      isInitialized
      version
    }
  }
`;
