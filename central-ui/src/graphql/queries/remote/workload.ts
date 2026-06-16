import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

const WORKLOAD_FIELDS_SLIM = `
  id {
    namespace
    kind
    name
  }
  serviceName
  dataStreamNames
  runtimeInfo {
    detectedLanguages
  }
  workloadOdigosHealthStatus {
    name
    status
    reasonEnum
    message
  }
  podsAgentInjectionStatus {
    name
    status
    reasonEnum
    message
  }
`;

const WORKLOAD_FIELDS = `
      id {
        namespace
        kind
        name
      }
      serviceName
      dataStreamNames
      numberOfInstances
      markedForInstrumentation {
        markedForInstrumentation
      }
      runtimeInfo {
        detectedLanguages
      }
      containers {
        containerName
        runtimeInfo {
          language
          runtimeVersion
        }
        agentEnabled {
          agentEnabled
          agentEnabledStatus {
            status
            reasonEnum
            message
          }
          otelDistroName
        }
        overrides {
          containerName
          runtimeInfo {
            language
            runtimeVersion
          }
        }
      }
      conditions {
        runtimeDetection {
          name
          status
          reasonEnum
          message
        }
        agentInjectionEnabled {
          name
          status
          reasonEnum
          message
        }
        rollout {
          name
          status
          reasonEnum
          message
        }
        agentInjected {
          name
          status
          reasonEnum
          message
        }
        processesAgentHealth {
          name
          status
          reasonEnum
          message
        }
        expectingTelemetry {
          name
          status
          reasonEnum
          message
        }
      }
      workloadOdigosHealthStatus {
        name
        status
        reasonEnum
        message
      }
      podsAgentInjectionStatus {
        name
        status
        reasonEnum
        message
      }
      rollbackOccurred`;

// Slim listing query: only fields needed for the sources table.
// Detailed payload (containers, conditions, agent status, rollback) lives in GET_WORKLOADS_BY_IDS.
// Kept as a separate shape from WORKLOAD_FIELDS to avoid unnecessary data-transfer on list calls.
export const GET_WORKLOADS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  query GetWorkloads($filter: WorkloadFilter) {
    workloads(filter: $filter) {
      ${WORKLOAD_FIELDS_SLIM}
    }
  }`,
    'v1.20': `
  query GetWorkloads($filter: WorkloadFilter) {
    workloads(filter: $filter) {
${WORKLOAD_FIELDS}
    }
  }`,
    'v1.17': `
  query GetWorkloads($filter: WorkloadFilter) {
    workloads(filter: $filter) {
      id {
        namespace
        kind
        name
      }
      workloadOdigosHealthStatus {
        name
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
  query GetWorkloads($filter: WorkloadFilter) {
    workloads(filter: $filter) {
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
};

export const GET_WORKLOADS_BY_IDS_SLIM: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.24': `
  query GetWorkloadsByIdsSlim($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
      ${WORKLOAD_FIELDS_SLIM}
    }
  }`,
    'v1.20': `
  query GetWorkloadsByIdsSlim($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
      ${WORKLOAD_FIELDS}
    }
  }`,
  },
};

export const GET_WORKLOADS_BY_IDS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.30': `
  query GetWorkloadsByIds($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
      id {
        namespace
        kind
        name
      }
      serviceName
      dataStreamNames
      numberOfInstances
      rollbackOccurred
      workloadOdigosHealthStatus {
        name
        status
        reasonEnum
        message
      }
      podsAgentInjectionStatus {
        name
        status
        reasonEnum
        message
      }
      podsOdigosHealthStatus {
        name
        status
        reasonEnum
        message
      }
      markedForInstrumentation {
        markedForInstrumentation
      }
      conditions {
        runtimeDetection {
          name
          status
          reasonEnum
          message
        }
        agentInjectionEnabled {
          name
          status
          reasonEnum
          message
        }
        rollout {
          name
          status
          reasonEnum
          message
        }
        autoRollback {
          name
          status
          reasonEnum
          message
        }
        agentInjected {
          name
          status
          reasonEnum
          message
        }
        processesAgentHealth {
          name
          status
          reasonEnum
          message
        }
        expectingTelemetry {
          name
          status
          reasonEnum
          message
        }
      }
      autoRollback {
        autoRollbackStatus {
          name
          status
          reasonEnum
          message
        }
        rollbackOccurred
      }
      runtimeInfo {
        detectedLanguages
      }
      containers {
        containerName
        runtimeInfo {
          language
          runtimeVersion
        }
        agentEnabled {
          agentEnabled
          agentEnabledStatus {
            status
            reasonEnum
            message
          }
          otelDistroName
        }
        overrides {
          containerName
          otelDistroName
          runtimeInfo {
            language
            runtimeVersion
          }
        }
        agentConfig {
          traces {
            headSampling {
              dryRun
              spanMetricsMode
              noisyOperations {
                ruleId
                name
                disabled
                operation {
                  httpServer {
                    route
                    routePrefix
                    method
                  }
                  httpClient {
                    serverAddress
                    templatedPath
                    templatedPathPrefix
                    method
                  }
                }
                percentageAtMost
              }
            }
          }
        }
        collectorConfig {
          tailSampling {
            noisyOperations {
              ruleId
              name
              disabled
              operation {
                httpServer {
                  route
                  routePrefix
                  method
                }
                httpClient {
                  serverAddress
                  templatedPath
                  templatedPathPrefix
                  method
                }
              }
              percentageAtMost
            }
            highlyRelevantOperations {
              ruleId
              name
              disabled
              error
              durationAtLeastMs
              operation {
                httpServer {
                  route
                  routePrefix
                  method
                }
                kafkaConsumer {
                  kafkaTopic
                }
                kafkaProducer {
                  kafkaTopic
                }
              }
              percentageAtLeast
            }
            costReductionRules {
              ruleId
              name
              disabled
              operation {
                httpServer {
                  route
                  routePrefix
                  method
                }
                kafkaConsumer {
                  kafkaTopic
                }
                kafkaProducer {
                  kafkaTopic
                }
              }
              percentageAtMost
            }
          }
        }
      }
      pods {
        podName
        nodeName
        startTime
        agentInjected
        agentInjectedStatus {
          name
          status
          reasonEnum
          message
        }
        k8sHealthStatus {
          name
          status
          reasonEnum
          message
        }
        odigosHealthStatus {
          name
          status
          reasonEnum
          message
        }
        containers {
          containerName
          otelDistroName
          started
          ready
          isCrashLoop
          restartCount
          runningStartedTime
          waitingReasonEnum
          waitingMessage
          k8sHealthStatus {
            name
            status
            reasonEnum
            message
          }
          odigosHealthStatus {
            name
            status
            reasonEnum
            message
          }
          processes {
            healthy
            healthStatus {
              name
              status
              reasonEnum
              message
            }
            identifyingAttributes {
              name
              value
            }
            instrumentations {
              name
              healthy
              message
              isStandardLibrary
            }
          }
        }
      }
      telemetryMetrics {
        throughputBytes
      }
    }
  }`,
    'v1.20': `
  query GetWorkloadsByIds($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
${WORKLOAD_FIELDS}
    }
  }`,
  },
};
