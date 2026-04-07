import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const UPDATE_LOCAL_UI_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation UpdateLocalUiConfig($config: LocalUiConfigInput!) {
    updateLocalUiConfig(config: $config)
  }
`,
  },
};
