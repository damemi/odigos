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

// VM-only: resolved from the vmagent `profileHotFunctions` dialect document
// (see VmSpec on GET_PROFILE_HOT_FUNCTIONS in odigos-api-adapter). No k8s bucket.
export const GET_PROFILE_HOT_FUNCTIONS: VersionedRemoteFetch = {};
