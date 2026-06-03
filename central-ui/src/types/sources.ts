import type { Condition, Workload, WorkloadId } from '@odigos/ui-kit/types';

export interface SourceInstrumentInput {
  sources: {
    namespace: string;
    name: string;
    kind: string;
    selected: boolean;
    currentStreamName: string;
  }[];
}

export type SourceConditions = WorkloadId & { conditions: Condition[] };

// Extended Workload type that includes workloadOdigosHealthStatus for v1.16+ API
export type WorkloadWithOdigosHealthStatus = Workload;
