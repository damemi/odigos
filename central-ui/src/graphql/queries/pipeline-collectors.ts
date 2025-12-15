export const GET_GATEWAY_INFO = `
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
          lastTransitionTime
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
  }
`;

export const GET_GATEWAY_PODS = `
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
  }
`;

export const GET_NODE_COLLECTOR_INFO = `
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
  }
`;

export const GET_NODE_COLLECTOR_PODS = `
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
  }
`;

export const GET_COLLECTOR_POD_INFO = `
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
  }
`;
