export const GET_CONFIG = `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }
`;

export const GET_ODIGOS_CONFIG = `
  query GetOdigosConfig {
    odigosConfig {
      karpenterEnabled
      allowConcurrentAgents
      uiPaginationLimit
      centralBackendURL
      oidc {
        tenantUrl
        clientId
        clientSecret
      }
      clusterName
      imagePrefix
      ignoredNamespaces
      ignoredContainers
      mountMethod
      agentEnvVarsInjectionMethod
      customContainerRuntimeSocketPath
      odigletHealthProbeBindPort
      rollbackDisabled
      rollbackGraceTime
      rollbackStabilityWindow
      nodeSelector
      rollout {
        automaticRolloutDisabled
      }
      collectorNode {
        collectorOwnMetricsPort
        requestMemoryMiB
        limitMemoryMiB
        requestCPUm
        limitCPUm
        memoryLimiterLimitMiB
        memoryLimiterSpikeLimitMiB
        goMemLimitMiB
        k8sNodeLogsDirectory
      }
      collectorGateway {
        requestMemoryMiB
        limitMemoryMiB
        requestCPUm
        limitCPUm
        memoryLimiterLimitMiB
        memoryLimiterSpikeLimitMiB
        goMemLimitMiB
        minReplicas
        maxReplicas
      }
    }
  }
`;
