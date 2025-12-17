import type { VersionedRemoteFetch } from '@/types';

export const UPDATE_TOKEN: VersionedRemoteFetch = {
  'v1.11': `
  mutation UpdateApiToken($token: String!) {
    updateApiToken(token: $token)
  }
`,
};
