import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const UPDATE_LOCAL_UI_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // `updateLocalUiConfig` / `LocalUiConfigInput` landed with the settings
    // page in v1.24 (#4484). Older proxies don't define them, so gate at v1.24
    // (matches RESET_LOCAL_UI_CONFIG_TO_FACTORY_DEFAULTS below) instead of
    // sending a mutation the proxy rejects.
    'v1.24': `
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
