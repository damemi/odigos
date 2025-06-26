export const PERSIST_NAMESPACES = `
  mutation PersistNamespaces($namespaces: [PersistNamespaceItemInput!]!) {
    persistK8sNamespaces(namespaces: $namespaces)
  }
`;
