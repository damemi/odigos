export interface VersionedRemoteFetch {
  'v1.13'?: string;
  'v1.12'?: string;
  'v1.11'?: string;
}

export interface PaginatedData<T = unknown> {
  nextPage: string;
  items: T[];
}
