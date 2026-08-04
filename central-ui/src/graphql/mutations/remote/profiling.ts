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

// VM-only: resolved from the vmagent `clearProfileCache` dialect document
// (see VmSpec on CLEAR_PROFILE_CACHE in odigos-api-adapter). No k8s bucket.
export const CLEAR_PROFILE_CACHE: VersionedRemoteFetch = {};
