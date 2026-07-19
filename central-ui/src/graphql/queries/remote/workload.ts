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
    // `workloadsByIds` / `K8sWorkloadIdInput` were introduced in v1.21. Older
    // proxies use the legacy per-id `run` path in the adapter; v1.20 has no
    // workloadsByIds query, so the lowest modern bucket must be v1.21.
    'v1.21': `
  query GetWorkloadsByIdsSlim($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
      ${WORKLOAD_FIELDS}
    }
  }`,
  },
};

const DESIRED_CONDITION_FIELDS = `
          name
          status
          reasonEnum
          message
`;

// actionItems shipped with status-action-item on top of podsManifestInjection (PR #5236).
// Only request them on the v1.33+ selection set so older proxies do not reject the query.
const DESIRED_CONDITION_FIELDS_WITH_ACTIONS = `
          name
          status
          reasonEnum
          message
          actionItems {
            type
            buttonText
          }
`;

const PODS_MANIFEST_INJECTION_CONDITION_FIELDS = `
        podsManifestInjection {
${DESIRED_CONDITION_FIELDS_WITH_ACTIONS}
        }
`;

const ROLLOUT_FIELDS = `
      rollout {
        rolloutStatus {
${DESIRED_CONDITION_FIELDS}
        }
        agentsMetaHashChangedTime
        podsManifestInjectionStatus {
${DESIRED_CONDITION_FIELDS_WITH_ACTIONS}
        }
        podsManifestInjectionOverview {
          totalPods
          totalAgentNotAppliedPods
          agentNotAppliedOk
          totalAgentAppliedPods
          agentAppliedOk
          totalAgentOutOfDatePods
          agentOutOfDateOk
        }
      }
`;

const getWorkloadsByIdsDetailedQuery = (headNoisyOperationFields: string, collectorNoisyHttpServerFields: string, includePodsManifestInjection = false) => `
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
${includePodsManifestInjection ? PODS_MANIFEST_INJECTION_CONDITION_FIELDS : ''}
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
${includePodsManifestInjection ? ROLLOUT_FIELDS : ''}
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
${headNoisyOperationFields}
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
${collectorNoisyHttpServerFields}
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
        instrumentations {
          name
          healthy
          message
          isStandardLibrary
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
  }`;

const HEAD_NOISY_OPERATION_FIELDS_V129 = `
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
                  }`;

const HEAD_NOISY_OPERATION_FIELDS_V132 = `
                  httpServer {
                    route
                    routePrefix
                    method
                    queryParams {
                      name
                      valueExact
                    }
                  }
                  httpClient {
                    serverAddress
                    templatedPath
                    templatedPathPrefix
                    method
                  }`;

const COLLECTOR_NOISY_HTTP_SERVER_FIELDS_V129 = `
                  route
                  routePrefix
                  method`;

const COLLECTOR_NOISY_HTTP_SERVER_FIELDS_V132 = `
                  route
                  routePrefix
                  method
                  queryParams {
                    name
                    valueExact
                  }`;

export const GET_WORKLOADS_BY_IDS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    // HTTP queryParams head-sampling matchers landed in odigos v1.32 (after v1.31.2).
    // Older proxies reject that selection set.
    // (The gRPC matchers that briefly shipped alongside were reverted out of the schema in odigos-io/odigos#5239, so only queryParams remain in this bucket.)
    'v1.32': getWorkloadsByIdsDetailedQuery(HEAD_NOISY_OPERATION_FIELDS_V132, COLLECTOR_NOISY_HTTP_SERVER_FIELDS_V132),
    // podsManifestInjection / rollout.podsManifestInjectionStatus (+ actionItems) landed with
    // odigos PR #5236 and status-action-item. Older proxies reject those fields — keep them off until v1.33+.
    'v1.33': getWorkloadsByIdsDetailedQuery(HEAD_NOISY_OPERATION_FIELDS_V132, COLLECTOR_NOISY_HTTP_SERVER_FIELDS_V132, true),
    // `workloadsByIds` / `K8sWorkloadIdInput` were introduced in v1.21 (v1.20 has neither).
    // Pre-v1.21 proxies resolve via the legacy per-id `run` path.
    'v1.29': getWorkloadsByIdsDetailedQuery(HEAD_NOISY_OPERATION_FIELDS_V129, COLLECTOR_NOISY_HTTP_SERVER_FIELDS_V129),
    'v1.21': `
  query GetWorkloadsByIds($ids: [K8sWorkloadIdInput!]!) {
    workloadsByIds(ids: $ids) {
${WORKLOAD_FIELDS}
    }
  }`,
  },
};
