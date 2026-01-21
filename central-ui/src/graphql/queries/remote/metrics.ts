import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_METRICS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
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
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
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
  }`,
  },
};
