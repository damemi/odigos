import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const UPDATE_REMOTE_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.16': `
  mutation UpdateRemoteConfig($config: RemoteConfigInput!) {
    updateRemoteConfig(config: $config)
  }
`,
  },
};
