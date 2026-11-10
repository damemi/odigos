import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// @deprecated-legacy-api(v1.20) - combined legacy "namespaces with workloads"
// query used by the central-ui adapter when the proxy is < v1.20. The v1.20+
// `namespaces { workloads }` field does not exist on legacy proxies; instead
// `K8sActualNamespace` exposes a nested `sources` list. Requesting it inline
// here lets the kit's single declarative `useApiQuery('GET_NAMESPACES_WITH_WORKLOADS')`
// be satisfied with one round-trip (the old hook did an N+1: all-namespaces
// then one query per namespace). The adapter maps the legacy shape into the
// v1.20+ `Namespace` shape via `legacyNamespaceToNamespace`.
export const GET_NAMESPACES_WITH_SOURCES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetNamespacesWithSources {
    computePlatform {
      k8sActualNamespaces {
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
  // VM proxies synthesize namespaces from the versioned vmagent `GetSources`
  // dialect document (see the VmSpec on GET_NAMESPACES_WITH_WORKLOADS in
  // odigos-api-adapter), not from a hand-written bucket here.
};

export const GET_NAMESPACES_WITH_WORKLOADS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.20': `
  query GetNamespacesWithWorkloads {
    namespaces {
      name
      markedForInstrumentation
      dataStreamNames
      workloads {
        id {
          namespace
          kind
          name
        }
        markedForInstrumentation {
          markedForInstrumentation
        }
        dataStreamNames
        numberOfInstances
      }
    }
  }`,
  },
};
