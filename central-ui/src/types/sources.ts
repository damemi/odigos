import type { SelectedSource } from '@odigos/ui-kit/store';
import type { Condition, Source, Workload, WorkloadId } from '@odigos/ui-kit/types';

export interface SourceInstrumentInput {
  sources: Omit<SelectedSource, 'numberOfInstances'>[];
}

export type SourceConditions = WorkloadId & { conditions: Condition[] };

// Extended Workload type that includes workloadOdigosHealthStatus for v1.16+ API
export type WorkloadWithOdigosHealthStatus = Workload & {
  workloadOdigosHealthStatus?: Source['workloadOdigosHealthStatus'];
  rollbackOccurred?: boolean;
};
