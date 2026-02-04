import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_SOURCES: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetSources {
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
          lastTransitionTime
        }
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetSources {
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
          lastTransitionTime
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
          lastTransitionTime
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
          lastTransitionTime
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
          lastTransitionTime
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
          lastTransitionTime
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

export const GET_SOURCE_CONDITIONS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetSourceConditions {
    sourceConditions {
      namespace
      name
      kind
      conditions {
        status
        type
        reason
        message
        lastTransitionTime
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
