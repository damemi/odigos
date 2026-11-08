import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const ENABLE_SOURCE_PROFILING: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.25': `
  mutation EnableSourceProfiling($namespace: String!, $kind: String!, $name: String!) {
    enableSourceProfiling(namespace: $namespace, kind: $kind, name: $name) {
      status
      sourceKey
      maxSlots
      activeSlots
    }
  }`,
  },
};

export const DISABLE_SOURCE_PROFILING: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.25': `
  mutation DisableSourceProfiling($namespace: String!, $kind: String!, $name: String!) {
    disableSourceProfiling(namespace: $namespace, kind: $kind, name: $name) {
      status
      sourceKey
      activeSlots
    }
  }`,
  },
};

// The profiling page's slot TTL; the mutation joined the schema in odigos v1.33.
export const CONFIGURE_PROFILING_CACHE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.33': `
  mutation ConfigureProfilingCache($slotTTLSeconds: Int!) {
    configureProfilingCache(slotTTLSeconds: $slotTTLSeconds) {
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

// VM-only: resolved from the vmagent `clearProfileCache` dialect document
// (see VmSpec on CLEAR_PROFILE_CACHE in odigos-api-adapter). No k8s bucket.
export const CLEAR_PROFILE_CACHE: VersionedRemoteFetch = {};
