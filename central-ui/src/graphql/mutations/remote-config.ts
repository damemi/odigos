import type { VersionedRemoteFetch } from '@/types';

export const UPDATE_REMOTE_CONFIG: VersionedRemoteFetch = {
  'v1.16': `
  mutation UpdateRemoteConfig($config: RemoteConfigInput!) {
    updateRemoteConfig(config: $config)
  }
`,
};
