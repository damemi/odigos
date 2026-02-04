import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_TOKENS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.17': `
  query GetTokens {
    computePlatform {
      apiTokens {
        token
        name
        issuedAt
        expiresAt
        message
      }
    }
  }`,
    'v1.11': `
  query GetTokens {
    computePlatform {
      apiTokens {
        token
        name
        issuedAt
        expiresAt
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetTokens {
    computePlatform {
      apiTokens {
        token
        name
        issuedAt
        expiresAt
      }
    }
  }`,
  },
};
