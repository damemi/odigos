import type { VersionedRemoteFetch } from '@/types';
import { PlatformType } from '@odigos/ui-kit/types';

export const UPDATE_TOKEN: VersionedRemoteFetch = {
  [PlatformType.K8s]: {
    'v1.11': `
  mutation UpdateApiToken($token: String!) {
    updateApiToken(token: $token)
  }`,
  },
  [PlatformType.Vm]: {
    'v0.1': `
  mutation UpdateApiToken($token: String!) {
    updateApiToken(token: $token)
  }`,
  },
};
