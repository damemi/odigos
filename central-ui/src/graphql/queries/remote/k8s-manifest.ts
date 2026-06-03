import { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_K8S_MANIFEST: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.20': `
  query GetK8sManifest($namespace: String!, $kind: K8sResourceKind!, $name: String!) {
    k8sManifest(namespace: $namespace, kind: $kind, name: $name)
  }`,
  },
};
