import { PlatformType } from '@odigos/ui-kit/types';

export interface VersionedRemoteFetch {
  [PlatformType.K8s]?: {
    'v1.19'?: string;
    'v1.17'?: string;
    'v1.16'?: string;
    'v1.15'?: string;
    'v1.13'?: string;
    'v1.12'?: string;
    'v1.11'?: string;
  };
  [PlatformType.Vm]?: {
    'v0.1'?: string;
  };
}

export interface PaginatedData<T = unknown> {
  nextPage: string;
  items: T[];
}
