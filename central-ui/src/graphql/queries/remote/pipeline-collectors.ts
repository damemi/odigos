import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_GATEWAY_INFO: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.12': `
  query GatewayDeploymentInfo {
    gatewayDeploymentInfo {
      status
      hpa {
        min
        max
        current
        desired
        conditions {
          status
          type
          reason
          message

        }
      }
      resources {
        requests {
          cpu
          memory
        }
        limits {
          cpu
          memory
        }
      }
      imageVersion
      lastRolloutAt
      rolloutInProgress
      manifestYAML
      configMapYAML
    }
  }`,
  },
};

export const GET_GATEWAY_PODS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.13': `
  query GatewayPods {
    gatewayPods {
      namespace
      name
      status
      restartsCount
      nodeName
      creationTimestamp
      image
    }
  }`,
    'v1.12': `
  query GatewayPods {
    gatewayPods {
      namespace
      name
      ready
      started
      status
      restartsCount
      nodeName
      creationTimestamp
      image
    }
  }`,
  },
};

export const GET_NODE_COLLECTOR_INFO: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.12': `
  query OdigletDaemonSetInfo {
    odigletDaemonSetInfo {
      status
      nodes {
        desired
        ready
      }
      resources {
        requests {
          cpu
          memory
        }
        limits {
          cpu
          memory
        }
      }
      imageVersion
      lastRolloutAt
      rolloutInProgress
      manifestYAML
      configMapYAML
    }
  }`,
  },
};

export const GET_NODE_COLLECTOR_PODS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.13': `
  query OdigletPods {
    odigletPods {
      namespace
      name
      status
      restartsCount
      nodeName
      creationTimestamp
      image
    }
  }`,
    'v1.12': `
  query OdigletPods {
    odigletPods {
      namespace
      name
      ready
      started
      status
      restartsCount
      nodeName
      creationTimestamp
      image
    }
  }`,
  },
};

export const GET_COLLECTOR_POD_INFO: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.13': `
  query GetCollectorPodDetails($namespace: String!, $name: String!) {
    collectorPod(namespace: $namespace, name: $name) {
      namespace
      name
      node
      status
      containers {
        name
        image
        status
        stateReason
        ready
        started
        restarts
        startedAt
        resources {
          requests {
            cpu
            memory
          }
          limits {
            cpu
            memory
          }
        }
      }
      manifestYAML
    }
  }`,
    'v1.12': `
  query GetCollectorPodDetails($namespace: String!, $name: String!) {
    collectorPod(namespace: $namespace, name: $name) {
      namespace
      name
      node
      status
      containers {
        name
        image
        status
        stateReason
        ready
        restarts
        startedAt
        resources {
          requests {
            cpu
            memory
          }
          limits {
            cpu
            memory
          }
        }
      }
      manifestYAML
    }
  }`,
  },
};
