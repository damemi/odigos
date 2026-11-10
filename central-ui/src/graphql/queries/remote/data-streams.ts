import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const GET_DATA_STREAMS: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  query GetDataStreams {
    computePlatform {
      dataStreams {
        name
      }
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  query GetDataStreams {
    computePlatform {
      dataStreams {
        name
      }
    }
  }`,
  },
};
