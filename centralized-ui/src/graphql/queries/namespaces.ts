export const GET_NAMESPACES = `
  query GetNamespaces {
    computePlatform {
      k8sActualNamespaces {
        name
        selected
      }
    }
  }
`;

export const GET_NAMESPACE = `
  query GetNamespace($namespaceName: String!) {
    computePlatform {
      k8sActualNamespace(name: $namespaceName) {
        name
        selected
        sources {
          kind
          name
          numberOfInstances
          selected
        }
      }
    }
  }
`;
