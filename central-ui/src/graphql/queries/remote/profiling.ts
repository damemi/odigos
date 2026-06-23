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
  query GetSourceProfiling($namespace: String!, $kind: K8sResourceKind!, $name: String!) {
    computePlatform {
      source(sourceId: { namespace: $namespace, kind: $kind, name: $name }) {
        profiling {
          profileJson
        }
      }
    }
  }
`,
  },
};
