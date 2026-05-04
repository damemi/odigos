import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_PROFILING_SLOTS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.25': `
  query GetProfilingSlots {
    profilingSlots {
      activeKeys
      keysWithData
      totalBytesUsed
      slotMaxBytes
      maxSlots
      maxTotalBytesBudget
      slotTtlSeconds
    }
  }`,
  },
};

export const GET_SOURCE_PROFILING: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.25': `
  query GetSourceProfiling($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        profiling {
          profileJson
        }
      }
    }
  }
`,
  },
};
