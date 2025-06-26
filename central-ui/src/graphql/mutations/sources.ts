export const PERSIST_SOURCES = `
  mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
    persistK8sSources(sources: $sources)
  }
`;

export const UPDATE_SOURCE = `
  mutation UpdateK8sActualSource($sourceId: K8sSourceId!, $patchSourceRequest: PatchSourceRequestInput!) {
    updateK8sActualSource(sourceId: $sourceId, patchSourceRequest: $patchSourceRequest)
  }
`;

export const RESTART_WORKLOADS = `
  mutation RestartWorkloads($sourceIds: [K8sSourceId!]!) {
    restartWorkloads(sourceIds: $sourceIds)
  }
`;
