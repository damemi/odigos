import type { VersionedRemoteFetch } from '@/types';

export const PERSIST_SOURCES: VersionedRemoteFetch = {
  'v1.11': `
  mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
    persistK8sSources(sources: $sources)
  }
`,
};

export const UPDATE_SOURCE: VersionedRemoteFetch = {
  'v1.11': `
  mutation UpdateK8sActualSource($sourceId: K8sSourceId!, $patchSourceRequest: PatchSourceRequestInput!) {
    updateK8sActualSource(sourceId: $sourceId, patchSourceRequest: $patchSourceRequest)
  }
`,
};

export const RESTART_WORKLOADS: VersionedRemoteFetch = {
  'v1.11': `
  mutation RestartWorkloads($sourceIds: [K8sSourceId!]!) {
    restartWorkloads(sourceIds: $sourceIds)
  }
`,
};

export const RESTART_POD: VersionedRemoteFetch = {
  'v1.12': `
  mutation RestartPod($namespace: String!, $name: String!) {
    restartPod(namespace: $namespace, name: $name)
  }
`,
};
