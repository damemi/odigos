export const GET_NAMESPACES = `
  query GetNamespaces {
    computePlatform {
      k8sActualNamespaces {
        name
        selected
        dataStreamNames
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
        dataStreamNames
        sources {
          namespace
          kind
          name
          dataStreamNames
          selected
          numberOfInstances
        }
      }
    }
  }
`;
