import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_NAMESPACES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetNamespaces {
    computePlatform {
      k8sActualNamespaces {
        name
        selected
        dataStreamNames
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetNamespaces {
    computePlatform {
      k8sActualNamespaces {
        name
        selected
        dataStreamNames
      }
    }
  }`,
  },
};

export const GET_NAMESPACE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
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
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
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
  }`,
  },
};
