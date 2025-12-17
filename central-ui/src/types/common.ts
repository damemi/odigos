export interface VersionedRemoteFetch {
  'v1.11'?: string;
  'v1.12'?: string;
}

export interface PaginatedData<T = unknown> {
  nextPage: string;
  items: T[];
}
