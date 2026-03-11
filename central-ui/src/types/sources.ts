import type { SelectedSource } from '@odigos/ui-kit/store';
import type { Condition, DesiredConditionStatus, Source, Workload, WorkloadId } from '@odigos/ui-kit/types';

export interface SourceInstrumentInput {
  sources: Omit<SelectedSource, 'numberOfInstances'>[];
}

export type SourceConditions = WorkloadId & { conditions: Condition[] };

// Extended Workload type that includes workloadOdigosHealthStatus for v1.16+ API
export type WorkloadWithOdigosHealthStatus = Workload & {
  workloadOdigosHealthStatus?: Source['workloadOdigosHealthStatus'];
  rollbackOccurred?: boolean;
};

export interface K8sWorkloadRuntimeInfo {
  language: string;
  runtimeVersion: string | null;
}

export interface K8sWorkloadAgentEnabled {
  agentEnabled: boolean;
  agentEnabledStatus: { message: string };
  otelDistroName: string | null;
}

export interface K8sWorkloadContainerResponse {
  containerName: string;
  runtimeInfo: K8sWorkloadRuntimeInfo | null;
  agentEnabled: K8sWorkloadAgentEnabled | null;
  overrides: { containerName: string } | null;
}

export interface K8sWorkloadConditions {
  runtimeDetection: DesiredConditionStatus | null;
  agentInjectionEnabled: DesiredConditionStatus | null;
  rollout: DesiredConditionStatus | null;
  agentInjected: DesiredConditionStatus | null;
  processesAgentHealth: DesiredConditionStatus | null;
  expectingTelemetry: DesiredConditionStatus | null;
}

export interface WorkloadResponse {
  id: WorkloadId;
  serviceName: string | null;
  dataStreamNames: string[];
  numberOfInstances: number | null;
  markedForInstrumentation: { markedForInstrumentation: boolean | null };
  runtimeInfo: { detectedLanguages: string[] | null } | null;
  containers: K8sWorkloadContainerResponse[] | null;
  conditions: K8sWorkloadConditions | null;
  workloadOdigosHealthStatus: DesiredConditionStatus | null;
  podsAgentInjectionStatus: DesiredConditionStatus | null;
  rollbackOccurred: boolean;
}
