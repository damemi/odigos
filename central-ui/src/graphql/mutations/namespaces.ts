import type { VersionedRemoteFetch } from '@/types';

export const PERSIST_NAMESPACES: VersionedRemoteFetch = {
  'v1.11': `
  mutation PersistNamespaces($namespaces: [PersistNamespaceItemInput!]!) {
    persistK8sNamespaces(namespaces: $namespaces)
  }
`,
};
