import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// @deprecated-legacy-api(v1.20) - combined legacy "sources list" query used by
// the central-ui adapter when the proxy is < v1.20. It folds the old
// multi-round-trip flow into a single REMOTE_FETCH query so the kit's
// declarative `useApiQuery('GET_WORKLOADS')` subscription (and the imperative
// `fetchAll`) can both consume it: `computePlatform.sources` carries the flat
// `K8sActualSource` shape (containers + conditions), while the root
// `workloads(filter)` selection carries the agent-injection / health status
// that the flat sources shape lacks. The adapter maps + merges these into the
// v1.20+ `Workload` shape via `mapLegacySourceToWorkload` +
// `addAgentInjectionStatusToSources`. VM proxies expose no `workloads` field,
// so the VM bucket is sources-only (mirrors the old `isVm` enrichment skip).
export const GET_SOURCES_WITH_STATUS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.17': `
  query GetSourcesWithStatus {
    computePlatform {
      sources {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message
        }
      }
    }
    workloads(filter: { markedForInstrumentation: true }) {
      id {
        namespace
        kind
        name
      }
      workloadOdigosHealthStatus {
        status
        reasonEnum
        message
      }
      podsAgentInjectionStatus {
        status
        message
      }
    }
  }`,
    'v1.11': `
  query GetSourcesWithStatus {
    computePlatform {
      sources {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message
        }
      }
    }
    workloads(filter: { markedForInstrumentation: true }) {
      id {
        namespace
        kind
        name
      }
      podsAgentInjectionStatus {
        status
        message
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetSourcesWithStatus {
    computePlatform {
      sources {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message
        }
      }
    }
  }`,
  },
};

export const GET_SOURCE: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.17': `
  query GetSource($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message

        }
        manifestYAML
        instrumentationConfigYAML
      }
    }
  }`,
    'v1.12': `
  query GetSource($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message

        }
        workloadOdigosHealthStatus {
          status
          reasonEnum
          message
        }
        manifestYAML
      }
    }
  }`,
    'v1.11': `
  query GetSource($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message

        }
        workloadOdigosHealthStatus {
          status
          reasonEnum
          message
        }
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetSource($sourceId: K8sSourceId!) {
    computePlatform {
      source(sourceId: $sourceId) {
        namespace
        name
        kind
        dataStreamNames
        selected
        otelServiceName
        containers {
          containerName
          language
          runtimeVersion
          overriden
          instrumented
          instrumentationMessage
          otelDistroName
        }
        conditions {
          status
          type
          reason
          message

        }
        workloadOdigosHealthStatus {
          status
          reasonEnum
          message
        }
      }
    }
  }`,
  },
};

export const GET_PEER_SOURCES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.21': `
  query GetPeerSources($serviceName: String!) {
    peerSources(serviceName: $serviceName) {
      inbound {
        serviceName
        requests
        dateTime
      }
      outbound {
        serviceName
        requests
        dateTime
      }
    }
  }`,
  },
};

export const GET_SOURCE_LIBRARIES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetSourceLibraries($namespace: String!, $kind: String!, $name: String!) {
    instrumentationInstanceComponents(namespace: $namespace, kind: $kind, name: $name) {
      name
      nonIdentifyingAttributes {
        key
        value
      }
    }
  }`,
  },
};
