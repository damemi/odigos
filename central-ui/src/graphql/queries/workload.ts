import { VersionedRemoteFetch } from '@/types';

// TODO: add all fields and migrate away from GET_SOURCES
export const GET_WORKLOADS: VersionedRemoteFetch = {
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
  }
`,
};
