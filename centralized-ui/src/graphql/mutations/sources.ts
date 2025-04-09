export const PERSIST_SOURCES = `
  mutation PersistSources($namespace: String!, $sources: [PersistNamespaceSourceInput!]!) {
    persistK8sSources(namespace: $namespace, sources: $sources)
  }
`

export const UPDATE_SOURCE = `
  mutation UpdateK8sActualSource($sourceId: K8sSourceId!, $patchSourceRequest: PatchSourceRequestInput!) {
    updateK8sActualSource(sourceId: $sourceId, patchSourceRequest: $patchSourceRequest)
  }
`
