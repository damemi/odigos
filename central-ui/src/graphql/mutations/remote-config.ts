import type { VersionedRemoteFetch } from '@/types';

export const UPDATE_REMOTE_CONFIG: VersionedRemoteFetch = {
  'v1.11': `
  mutation UpdateRemoteConfig($config: RemoteConfigInput!) {
    updateRemoteConfig(config: $config) {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`,
};
