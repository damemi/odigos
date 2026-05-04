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
