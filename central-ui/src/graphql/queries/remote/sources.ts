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
  // VM proxies resolve sources from the versioned `@odigos/vmagent-api-apollo-client`
  // dialect registry (see the VmSpec on GET_WORKLOADS in odigos-api-adapter), not
  // from a hand-written bucket here.
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
  // VM proxies resolve the by-name source from the versioned vmagent dialect
  // registry (see the VmSpec on GET_SOURCE in odigos-api-adapter).
};

// @deprecated-legacy-api(v1.20) - combined legacy "source by id" query used by
// the central-ui adapter when the proxy is < v1.20. `workloadsByIds` /
// `K8sWorkloadIdInput` only exist in v1.21+, so for older proxies we fold the
// old multi-round-trip drawer flow (GET_SOURCE_BY_ID + GET_SOURCE_CONDITIONS +
// GET_SOURCE_LIBRARIES + DESCRIBE_SOURCE + the workloads injection rows) into a
// SINGLE REMOTE_FETCH query and let the adapter's `transformResult` map + merge
// the pieces into the v1.20+ `Workload` shape (the output of
// `GET_WORKLOADS_BY_IDS`):
//   - `computePlatform.source` carries the flat `K8sActualSource` (containers +
//     inline conditions + service name).
//   - `sourceConditions` carries the additional workload-level conditions
//     (rollout / agent-injection-enabled / ...) that the flat source shape
//     lacks, keyed by namespace/kind/name.
//   - `describeSource` carries the live pod tree (pod → container → instrumentation
//     instances). On v1.20+ the pods come straight from `workloadsByIds`; on
//     legacy proxies `describeSource` is the only source of real pod data, so we
//     map it into the workload's `pods[]` instead of fabricating a synthetic pod.
//   - `instrumentationInstanceComponents` carries the detected instrumentation
//     libraries (attached to the mapped processes' `instrumentations`).
//   - `workloads(filter)` carries the agent-injection / health status.
// VM proxies expose none of `workloads` / `sourceConditions` / `describeSource` /
// `instrumentationInstanceComponents`, so the VM bucket is source-only (mirrors
// the old `isVm` enrichment skip). Remove this when min supported proxy >= v1.20.
export const GET_SOURCE_WITH_DETAILS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.17': `
  query GetSourceWithDetails($sourceId: K8sSourceId!, $namespace: String!, $kind: String!, $name: String!) {
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
      }
    }
    sourceConditions {
      namespace
      name
      kind
      conditions {
        status
        type
        reason
        message
      }
    }
    instrumentationInstanceComponents(namespace: $namespace, kind: $kind, name: $name) {
      name
      nonIdentifyingAttributes {
        key
        value
      }
    }
    describeSource(namespace: $namespace, kind: $kind, name: $name) {
      pods {
        podName { name value }
        nodeName { name value }
        agentInjected { name value }
        containers {
          containerName { name value }
          actualDevices { name value }
          started { name value }
          ready { name value }
          instrumentationInstances {
            healthy { name value }
            message { name value }
            identifyingAttributes { name value }
          }
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
  query GetSourceWithDetails($sourceId: K8sSourceId!, $namespace: String!, $kind: String!, $name: String!) {
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
    sourceConditions {
      namespace
      name
      kind
      conditions {
        status
        type
        reason
        message
      }
    }
    instrumentationInstanceComponents(namespace: $namespace, kind: $kind, name: $name) {
      name
      nonIdentifyingAttributes {
        key
        value
      }
    }
    describeSource(namespace: $namespace, kind: $kind, name: $name) {
      pods {
        podName { name value }
        nodeName { name value }
        agentInjected { name value }
        containers {
          containerName { name value }
          actualDevices { name value }
          started { name value }
          ready { name value }
          instrumentationInstances {
            healthy { name value }
            message { name value }
            identifyingAttributes { name value }
          }
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
    // VM proxies have no `workloads` / `sourceConditions` /
    // `instrumentationInstanceComponents` fields, so the VM bucket is
    // source-only and declares just `$sourceId` (the namespace/kind/name vars
    // the adapter also passes are simply ignored by the server here).
    'v0.1': `
  query GetSourceWithDetails($sourceId: K8sSourceId!) {
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
