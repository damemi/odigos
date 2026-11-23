import { PlatformType } from '@odigos/ui-kit/types';

export interface VersionedRemoteFetch {
  [PlatformType.K8s]?: {
    'v1.41'?: string;
    'v1.40'?: string;
    'v1.39'?: string;
    'v1.38'?: string;
    'v1.37'?: string;
    'v1.36'?: string;
    'v1.35'?: string;
    'v1.34'?: string;
    'v1.33'?: string;
    'v1.32'?: string;
    'v1.31'?: string;
    'v1.30'?: string;
    'v1.29'?: string;
    'v1.28'?: string;
    'v1.26'?: string;
    'v1.25'?: string;
    'v1.24'?: string;
    'v1.23'?: string;
    'v1.22'?: string;
    'v1.21'?: string;
    'v1.20'?: string;
    'v1.19'?: string;
    'v1.17'?: string;
    'v1.16'?: string;
    'v1.15'?: string;
    'v1.13'?: string;
    'v1.12'?: string;
    'v1.11'?: string;
  };
  // Per agents-api dialect (e.g. v0.6.0, v0.8.0, v0.10.0) or legacy v0.1 hand-written queries.
  [PlatformType.Vm]?: Record<string, string>;
  // Cloud connectors don't use versioned remote fetch (they have no proxy),
  // but the key keeps this type total over PlatformType for safe indexing.
  [PlatformType.Connector]?: Record<string, string>;
}

export interface PaginatedData<T = unknown> {
  nextPage: string;
  items: T[];
}
