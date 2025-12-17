import type { VersionedRemoteFetch } from '@/types';

export const GET_CONFIG: VersionedRemoteFetch = {
  'v1.11': `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }
`,
};
