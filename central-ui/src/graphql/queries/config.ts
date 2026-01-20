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

export const GET_EFFECTIVE_CONFIG: VersionedRemoteFetch = {
  'v1.16': `
  query GetEffectiveConfig {
    effectiveConfig {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`,
};
