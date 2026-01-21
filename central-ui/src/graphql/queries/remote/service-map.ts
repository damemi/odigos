import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_SERVICE_MAP: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetServiceMap {
    getServiceMap {
      services {
        serviceName
        services {
          serviceName
          requests
          dateTime
        }
      }
    }
  }`,
  },
};
