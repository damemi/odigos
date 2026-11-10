import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const UPDATE_DATA_STREAM: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation UpdateDataStream($id: ID!, $dataStream: DataStreamInput!) {
    updateDataStream(id: $id, dataStream: $dataStream) {
      name
    }
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation UpdateDataStream($id: ID!, $dataStream: DataStreamInput!) {
    updateDataStream(id: $id, dataStream: $dataStream) {
      name
    }
  }`,
  },
};

export const DELETE_DATA_STREAM: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation DeleteDataStream($id: ID!) {
    deleteDataStream(id: $id)
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation DeleteDataStream($id: ID!) {
    deleteDataStream(id: $id)
  }`,
  },
};
