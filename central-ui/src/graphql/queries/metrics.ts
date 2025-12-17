import type { VersionedRemoteFetch } from '@/types';

export const GET_METRICS: VersionedRemoteFetch = {
  'v1.11': `
  query GetOverviewMetrics {
    getOverviewMetrics {
      sources {
        namespace
        kind
        name
        totalDataSent
        throughput
      }
      destinations {
        id
        totalDataSent
        throughput
      }
    }
  }
`,
};
