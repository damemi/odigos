import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const PERSIST_SOURCES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
    persistK8sSources(sources: $sources)
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
    persistVmSources(sources: $sources)
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
  [PlatformType.Vm]: {
    'v0.1': `
  mutation UpdateSource($sourceId: SourceId!, $patchSourceRequest: PatchSourceRequestInput!) {
    updateSource(sourceId: $sourceId, patchSourceRequest: $patchSourceRequest)
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
