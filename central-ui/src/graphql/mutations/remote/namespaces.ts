import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const PERSIST_NAMESPACES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation PersistNamespaces($namespaces: [PersistNamespaceItemInput!]!) {
    persistK8sNamespaces(namespaces: $namespaces)
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation PersistNamespaces($namespaces: [PersistNamespaceItemInput!]!) {
    persistVmNamespaces(namespaces: $namespaces)
  }`,
  },
};
