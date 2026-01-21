import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }`,
  },
};

export const GET_EFFECTIVE_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.16': `
  query GetEffectiveConfig {
    effectiveConfig {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`,
  },
};
