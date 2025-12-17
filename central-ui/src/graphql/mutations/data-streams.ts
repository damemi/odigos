import type { VersionedRemoteFetch } from '@/types';

export const UPDATE_DATA_STREAM: VersionedRemoteFetch = {
  'v1.11': `
  mutation UpdateDataStream($id: ID!, $dataStream: DataStreamInput!) {
    updateDataStream(id: $id, dataStream: $dataStream) {
      name
    }
  }
`,
};

export const DELETE_DATA_STREAM: VersionedRemoteFetch = {
  'v1.11': `
  mutation DeleteDataStream($id: ID!) {
    deleteDataStream(id: $id)
  }
`,
};
