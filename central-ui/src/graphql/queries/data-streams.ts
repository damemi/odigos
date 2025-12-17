import type { VersionedRemoteFetch } from '@/types';

export const GET_DATA_STREAMS: VersionedRemoteFetch = {
  'v1.11': `
  query GetDataStreams {
    computePlatform {
      dataStreams {
        name
      }
    }
  }
`,
};
