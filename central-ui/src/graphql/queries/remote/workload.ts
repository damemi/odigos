import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

// TODO: add all fields and migrate away from GET_SOURCES
export const GET_WORKLOADS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
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
