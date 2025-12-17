import type { VersionedRemoteFetch } from '@/types';

export const GET_TOKENS: VersionedRemoteFetch = {
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
  }
`,
};
