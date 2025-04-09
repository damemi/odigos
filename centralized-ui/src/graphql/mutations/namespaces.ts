export const PERSIST_NAMESPACES = `
  mutation PersistNamespace($namespace: PersistNamespaceItemInput!) {
    persistK8sNamespace(namespace: $namespace)
  }
`
