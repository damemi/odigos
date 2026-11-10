import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_SERVICE_MAP: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.22': `
  query GetServiceMap {
    getServiceMap {
      services {
        nodeId
        serviceName
        services {
          nodeId
          isVirtual
          serviceName
          requests
          dateTime
        }
      }
    }
  }`,
  },
};
