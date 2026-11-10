import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const PERSIST_SOURCES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
    persistK8sSources(sources: $sources)
  }`,
  },
};

// Workloads and namespaces in one request, with `disableInstrumentation` per workload: the proxy
// creates those Sources already disabled, and applies workloads before namespaces so a disabled
// workload is never instrumented when its namespace's Auto turns on.
// 'v1.40' is a placeholder for the first Odigos release with this mutation (latest is v1.39.0);
// set it to that release once it is tagged.
export const PERSIST_SOURCES_AND_NAMESPACES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.40': `
  mutation PersistSourcesAndNamespaces($sources: [PersistNamespaceSourceInput!]!, $namespaces: [PersistNamespaceItemInput!]) {
    persistK8sSources(sources: $sources, namespaces: $namespaces)
  }`,
  },
};

export const UPDATE_SOURCE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation UpdateK8sActualSource($sourceId: K8sSourceId!, $patchSourceRequest: PatchSourceRequestInput!) {
    updateK8sActualSource(sourceId: $sourceId, patchSourceRequest: $patchSourceRequest)
  }`,
  },
};

export const RESTART_WORKLOADS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation RestartWorkloads($sourceIds: [K8sSourceId!]!) {
    restartWorkloads(sourceIds: $sourceIds)
  }`,
  },
};

export const RESTART_POD: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.12': `
  mutation RestartPod($namespace: String!, $name: String!) {
    restartPod(namespace: $namespace, name: $name)
  }`,
  },
};

export const RECOVER_FROM_ROLLBACK: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.20': `
  mutation RecoverFromRollbackForWorkload($sourceId: K8sSourceId!) {
    recoverFromRollbackForWorkload(sourceId: $sourceId)
  }`,
  },
};
