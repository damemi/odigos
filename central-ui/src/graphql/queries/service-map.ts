import type { VersionedRemoteFetch } from '@/types';

export const GET_SERVICE_MAP: VersionedRemoteFetch = {
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
  }
`,
};
