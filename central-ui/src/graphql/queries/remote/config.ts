import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetConfig {
    config {
      tier
      readonly
      installationMethod
      installationStatus
    }
  }`,
  },
};

export const GET_EFFECTIVE_CONFIG: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.25': `
  query GetEffectiveConfig {
    effectiveConfig {
      configVersion
      telemetryEnabled
      openshiftEnabled
      ignoredNamespaces
      ignoredContainers
      ignoreOdigosNamespace
      psp
      imagePrefix
      skipWebhookIssuerCreation
      collectorGateway {
        minReplicas
        maxReplicas
        requestMemoryMiB
        limitMemoryMiB
        requestCPUm
        limitCPUm
        memoryLimiterLimitMiB
        memoryLimiterSpikeLimitMiB
        goMemLimitMiB
        serviceGraphDisabled
        clusterMetricsEnabled
        httpsProxyAddress
        nodeSelector
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
        enableDataCompression
        otlpExporterConfiguration {
          enableDataCompression
          timeout
          retryOnFailure {
            enabled
            initialInterval
            maxInterval
            maxElapsedTime
          }
        }
      }
      profiles
      allowConcurrentAgents {
        enabled
      }
      uiMode
      uiPaginationLimit
      uiRemoteUrl
      centralBackendURL
      clusterName
      instrumentor {
        mountMethod
        agentEnvVarsInjectionMethod
        checkDeviceHealthBeforeInjection
      }
      customContainerRuntimeSocketPath
      userInstrumentationEnvs {
        languages
      }
      nodeSelector
      karpenter {
        enabled
      }
      rollout {
        automaticRolloutDisabled
        maxConcurrentRollouts
      }
      autoRollback {
        disabled
        graceTime
        stabilityWindowTime
      }
      oidc {
        tenantUrl
        clientId
        clientSecret
      }
      odigletHealthProbeBindPort
      goAutoOffsetsCron
      goAutoOffsetsMode
      clickhouseJsonTypeEnabled
      resourceSizePreset
      wasp {
        enabled
      }
      metricsSources {
        spanMetrics {
          disabled
          interval
          metricsExpiration
          additionalDimensions
          histogramDisabled
          histogramBuckets
          includedProcessInDimensions
          excludedResourceAttributes
          resourceMetricsKeyAttributes
        }
        hostMetrics {
          disabled
          interval
        }
        kubeletStats {
          disabled
          interval
        }
        odigosOwnMetrics {
          interval
        }
        agentMetrics {
          spanMetrics {
            enabled
          }
          runtimeMetrics {
            java {
              disabled
              metrics {
                name
                disabled
              }
            }
          }
        }
      }
      agentsInitContainerResources {
        requestCPUm
        limitCPUm
        requestMemoryMiB
        limitMemoryMiB
      }
      traceIdSuffix
      allowedTestConnectionHosts
      odigosOwnTelemetryStore {
        metricsStoreDisabled
      }
      imagePullSecrets
      componentLogLevels {
        default
        autoscaler
        scheduler
        instrumentor
        odiglet
        deviceplugin
        ui
        collector
      }
      sampling {
        dryRun
        spanSamplingAttributes {
          disabled
          samplingCategoryDisabled
          traceDecidingRuleDisabled
          spanDecisionAttributesDisabled
        }
        tailSampling {
          disabled
          traceAggregationWaitDuration
        }
        k8sHealthProbesSampling {
          enabled
          keepPercentage
        }
      }
      profiling {
        enabled
      }
      provenance {
        helmPath
        reconciledFrom
      }
      manifestYAML
    }
  }
`,
    'v1.24': `
  query GetEffectiveConfig {
    effectiveConfig {
      configVersion
      telemetryEnabled
      openshiftEnabled
      ignoredNamespaces
      ignoredContainers
      ignoreOdigosNamespace
      psp
      imagePrefix
      skipWebhookIssuerCreation
      collectorGateway {
        minReplicas
        maxReplicas
        requestMemoryMiB
        limitMemoryMiB
        requestCPUm
        limitCPUm
        memoryLimiterLimitMiB
        memoryLimiterSpikeLimitMiB
        goMemLimitMiB
        serviceGraphDisabled
        clusterMetricsEnabled
        httpsProxyAddress
        nodeSelector
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
        enableDataCompression
        otlpExporterConfiguration {
          enableDataCompression
          timeout
          retryOnFailure {
            enabled
            initialInterval
            maxInterval
            maxElapsedTime
          }
        }
      }
      profiles
      allowConcurrentAgents {
        enabled
      }
      uiMode
      uiPaginationLimit
      uiRemoteUrl
      centralBackendURL
      clusterName
      instrumentor {
        mountMethod
        agentEnvVarsInjectionMethod
        checkDeviceHealthBeforeInjection
      }
      customContainerRuntimeSocketPath
      userInstrumentationEnvs {
        languages
      }
      nodeSelector
      karpenter {
        enabled
      }
      rollout {
        automaticRolloutDisabled
      }
      autoRollback {
        disabled
        graceTime
        stabilityWindowTime
      }
      oidc {
        tenantUrl
        clientId
        clientSecret
      }
      odigletHealthProbeBindPort
      goAutoOffsetsCron
      goAutoOffsetsMode
      clickhouseJsonTypeEnabled
      resourceSizePreset
      wasp {
        enabled
      }
      metricsSources {
        spanMetrics {
          disabled
          interval
          metricsExpiration
          additionalDimensions
          histogramDisabled
          histogramBuckets
          includedProcessInDimensions
          excludedResourceAttributes
          resourceMetricsKeyAttributes
        }
        hostMetrics {
          disabled
          interval
        }
        kubeletStats {
          disabled
          interval
        }
        odigosOwnMetrics {
          interval
        }
        agentMetrics {
          spanMetrics {
            enabled
          }
          runtimeMetrics {
            java {
              disabled
              metrics {
                name
                disabled
              }
            }
          }
        }
      }
      agentsInitContainerResources {
        requestCPUm
        limitCPUm
        requestMemoryMiB
        limitMemoryMiB
      }
      traceIdSuffix
      allowedTestConnectionHosts
      odigosOwnTelemetryStore {
        metricsStoreDisabled
      }
      imagePullSecrets
      componentLogLevels {
        default
        autoscaler
        scheduler
        instrumentor
        odiglet
        deviceplugin
        ui
        collector
      }
      provenance {
        helmPath
        reconciledFrom
      }
      manifestYAML
    }
  }
`,
    'v1.16': `
  query GetEffectiveConfig {
    effectiveConfig {
      rollout {
        automaticRolloutDisabled
      }
    }
  }
`,
  },
};

export const GET_CONFIG_YAMLS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.26': `
    query GetConfigYamls {
      configYamls {
        name
        displayName
        fields {
          displayName
          componentType
          isHelmOnly
          isEnterpriseOnly
          description
          helmValuePath
          docsLink
          componentProps
          renderCondition
        }
      }
    }
  `,
    'v1.24': `
    query GetConfigYamls {
      configYamls {
        name
        displayName
        fields {
          displayName
          componentType
          isHelmOnly
          isEnterpriseOnly
          description
          helmValuePath
          docsLink
          componentProps
        }
      }
    }
  `,
    // `configYamls` was introduced in v1.23 (#4467). Proxies older than this
    // don't define the query at all, so there is no v1.11 bucket — older
    // proxies resolve to `undefined` and the op is gated off (no request).
    'v1.23': `
  query GetConfigYamls {
    configYamls {
      name
      displayName
      fields {
        displayName
        componentType
        isHelmOnly
        description
        helmValuePath
        docsLink
        componentProps
      }
    }
  }
`,
  },
};
