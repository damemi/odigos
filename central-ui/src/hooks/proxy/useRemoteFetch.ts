import { useCallback } from 'react';
import { useProxy } from './useProxy';
import { getVersionedQuery } from '@/utils';
import { useLazyQuery } from '@apollo/client';
import { StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { REMOTE_FETCH, REMOTE_MULTI_FETCH } from '@/graphql';
import type { ComputePlatform, VersionedRemoteFetch } from '@/types';
import { safeJsonParse, trimVersion } from '@odigos/ui-kit/functions';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyObj = Record<string, any>;

interface MultiFetchResult<T> {
  proxyID: string;
  success: boolean;
  error?: string;
  data?: T;
}

export interface MultiFetchResponse<T> {
  error?: string;
  results: MultiFetchResult<T>[];
  allSucceeded: boolean;
  anySucceeded: boolean;
  successCount: number;
  failureCount: number;
}

interface UseProxy<ResT, ReqT> {
  fetch: (variables?: ReqT, targetProxyID?: string) => Promise<{ data?: ResT; error?: string }>;
  fetchMulti: (proxyIDs: string[], variables?: ReqT) => Promise<MultiFetchResponse<ResT>>;
  called: boolean;
  loading: boolean;
  data?: ResT;
  dataMulti?: MultiFetchResult<ResT>[];
}

interface ParsedSingleResponse<ResT> {
  request_id: string;
  data?: ResT & { errors?: { message: string }[] };
  error?: string;
}

const EMPTY_RESPONSE_SINGLE: ParsedSingleResponse<any> = {
  request_id: '',
  data: undefined,
  error: undefined,
};

const EMPTY_RESPONSE_MULTI = <T>(error?: string): MultiFetchResponse<T> => ({
  error,
  results: [],
  allSucceeded: false,
  anySucceeded: false,
  successCount: 0,
  failureCount: 0,
});

const mapMultiFetchResult = <T>(result: MultiFetchResult<string>): MultiFetchResult<T> => ({
  proxyID: result.proxyID,
  success: result.success,
  data: result.data ? safeJsonParse<{ data?: T }>(result.data, { data: undefined }).data : undefined,
  error: result.error,
});

export const useRemoteFetch = <ResT = AnyObj, ReqT = AnyObj>(query: VersionedRemoteFetch, connections: ComputePlatform[] = []): UseProxy<ResT, ReqT> => {
  const { proxyID, odigosVersion, platformType } = useProxy();
  const { addNotification } = useNotificationStore();

  const [remoteFetch, { data: cachedData, loading, called }] = useLazyQuery<{ remoteFetch?: string }>(REMOTE_FETCH);
  const [remoteMultiFetch, { data: cachedMultiData, loading: multiLoading, called: multiCalled }] = useLazyQuery(REMOTE_MULTI_FETCH);

  const fetch: UseProxy<ResT, ReqT>['fetch'] = useCallback(
    async (variables, targetProxyID) => {
      const actualProxyId = targetProxyID || proxyID;
      if (!actualProxyId) {
        console.warn('Missing required "proxyID" for single-remote fetch!');
        return EMPTY_RESPONSE_SINGLE;
      }

      const actualPlatformType = platformType || connections.find((cp) => cp.id === actualProxyId)?.type;
      if (!actualPlatformType) {
        console.warn('Missing required "platformType" for single-remote fetch!');
        return EMPTY_RESPONSE_SINGLE;
      }

      const actualOdigosVersion = odigosVersion || connections.find((cp) => cp.id === actualProxyId)?.odigosVersion;
      if (!actualOdigosVersion) {
        console.warn('Missing required "odigosVersion" for single-remote fetch!');
        return EMPTY_RESPONSE_SINGLE;
      }

      const queryString = getVersionedQuery(query, actualPlatformType, actualOdigosVersion, addNotification);
      if (!queryString) {
        return EMPTY_RESPONSE_SINGLE;
      }

      const { error, data } = await remoteFetch({
        variables: {
          proxyID: actualProxyId,
          query: queryString,
          variables: variables || {},
        },
      });

      const parsedData = safeJsonParse<ParsedSingleResponse<ResT>>(data?.remoteFetch, EMPTY_RESPONSE_SINGLE);
      const errorFromApi = parsedData.data?.errors?.length ? parsedData.data.errors.map((e) => e.message).join(', ') : undefined;
      const errorFromApollo = error?.cause?.message || error?.message;
      const errorMessage = errorFromApi || errorFromApollo;

      if (errorMessage) {
        addNotification({ type: StatusType.Error, title: error?.name || 'API Error', message: errorMessage });
        return {
          ...EMPTY_RESPONSE_SINGLE,
          error: errorMessage,
        };
      }

      return parsedData;
    },
    [query, proxyID, platformType, odigosVersion, connections, remoteFetch, addNotification],
  );

  const fetchMulti: UseProxy<ResT, ReqT>['fetchMulti'] = useCallback(
    async (proxyIDs, variables) => {
      if (!proxyIDs.length) {
        console.warn('Missing required "proxyIDs" for multi-remote fetch!');
        return EMPTY_RESPONSE_MULTI<ResT>();
      }

      const targetConnections = connections.filter((cp) => proxyIDs.includes(cp.id));
      const versionedProxyIDs: {
        [key in keyof VersionedRemoteFetch]?: {
          proxyIDs: string[];
          queryString: string;
        };
      } = {};

      for (const { id, type, odigosVersion: vers } of targetConnections) {
        const queryString = getVersionedQuery(query, type, vers, addNotification);
        if (queryString) {
          const versionKey = `v${trimVersion(vers)}` as keyof VersionedRemoteFetch;

          if (!versionedProxyIDs[versionKey]) versionedProxyIDs[versionKey] = { queryString, proxyIDs: [] };
          versionedProxyIDs[versionKey]!.proxyIDs.push(id);
        }
      }

      const results: MultiFetchResult<ResT>[] = [];

      for await (const { proxyIDs, queryString } of Object.values(versionedProxyIDs)) {
        const { data, error } = await remoteMultiFetch({
          variables: {
            proxyIDs,
            query: queryString,
            variables: variables || {},
          },
        });

        if (error) {
          addNotification({ type: StatusType.Error, title: error.name || 'API Error', message: error.cause?.message || error.message });
          // return EMPTY_RESPONSE_MULTI<ResT>(error.cause?.message || error.message);
        }

        if (data) {
          results.push(...(data?.remoteMultiFetch || []).map(mapMultiFetchResult<ResT>));
        }
      }

      const successCount = results.filter((r) => r.success).length;
      const failureCount = results.length - successCount;

      return {
        results,
        allSucceeded: failureCount === 0,
        anySucceeded: successCount > 0,
        successCount,
        failureCount,
      };
    },
    [query, connections, remoteMultiFetch, addNotification],
  );

  return {
    fetch,
    fetchMulti,
    called: called || multiCalled,
    loading: loading || multiLoading,
    data: safeJsonParse<{ data?: ResT }>(cachedData?.remoteFetch, EMPTY_RESPONSE_SINGLE).data,
    dataMulti: cachedMultiData?.remoteMultiFetch ? cachedMultiData.remoteMultiFetch.map(mapMultiFetchResult<ResT>) : [],
  };
};
