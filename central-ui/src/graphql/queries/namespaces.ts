import type { VersionedRemoteFetch } from '@/types';

export const GET_NAMESPACES: VersionedRemoteFetch = {
  'v1.11': `
  query GetNamespaces {
    computePlatform {
      k8sActualNamespaces {
        name
        selected
        dataStreamNames
      }
    }
  }
`,
};

export const GET_NAMESPACE: VersionedRemoteFetch = {
  'v1.11': `
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
`,
};
