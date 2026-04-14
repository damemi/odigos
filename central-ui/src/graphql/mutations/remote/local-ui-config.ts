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

export const RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  mutation ResetLocalUiConfigToFactoryDefaults {
    resetLocalUiConfigToFactoryDefaults
  }
`,
  },
};
