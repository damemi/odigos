import type { VersionedRemoteFetch } from '@/types';

export const GET_REMOTE_CONFIG: VersionedRemoteFetch = {
  'v1.12': `
  query GetRemoteConfig {
    remoteConfig {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`,
};
