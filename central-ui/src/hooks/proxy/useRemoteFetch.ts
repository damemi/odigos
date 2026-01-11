import { useCallback } from 'react';
import { useProxy } from './useProxy';
import { getVersionedQuery } from '@/utils';
import { StatusType } from '@odigos/ui-kit/types';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { REMOTE_FETCH, REMOTE_MULTI_FETCH } from '@/graphql';
import { type ApolloError, useLazyQuery } from '@apollo/client';
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
  fetch: (variables?: ReqT, targetProxyID?: string) => Promise<{ data?: ResT }>;
  fetchMulti: (proxyIDs: string[], variables?: ReqT) => Promise<MultiFetchResponse<ResT>>;
  loading: boolean;
  error?: ApolloError;
  data?: ResT;
  dataMulti?: MultiFetchResponse<ResT>[];
}

const EMPTY_RESPONSE_SINGLE = {
  data: undefined,
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
  const { proxyID, odigosVersion } = useProxy();
  const { addNotification } = useNotificationStore();

  const [remoteFetch, { data: cachedData, loading }] = useLazyQuery(REMOTE_FETCH);
  const [remoteMultiFetch, { data: cachedMultiData, loading: multiLoading }] = useLazyQuery(REMOTE_MULTI_FETCH);

  const fetch: UseProxy<ResT, ReqT>['fetch'] = useCallback(
    async (variables, targetProxyID) => {
      const actualProxyId = targetProxyID || proxyID;
      if (!actualProxyId) {
        console.warn('Missing required "proxyID" for single-remote fetch!');
        return EMPTY_RESPONSE_SINGLE;
      }

      const actualOdigosVersion = odigosVersion || connections.find((cp) => cp.id === actualProxyId)?.odigosVersion;
      if (!actualOdigosVersion) {
        console.warn('Missing required "odigosVersion" for single-remote fetch!');
        return EMPTY_RESPONSE_SINGLE;
      }

      const queryString = getVersionedQuery(query, actualOdigosVersion, addNotification);
      if (!queryString) return EMPTY_RESPONSE_SINGLE;

      const { data, error } = await remoteFetch({
        variables: {
          proxyID: actualProxyId,
          query: queryString,
          variables: variables || {},
        },
      });

      if (error || !data?.remoteFetch) {
        const errorMessage = error?.cause?.message || error?.message;
        if (errorMessage) addNotification({ type: StatusType.Error, title: error?.name || 'API Error', message: errorMessage });
        return {
          ...EMPTY_RESPONSE_SINGLE,
          error: errorMessage,
        };
      } else {
        return safeJsonParse(data?.remoteFetch, EMPTY_RESPONSE_SINGLE);
      }
    },
    [query, proxyID, odigosVersion, connections, remoteFetch, addNotification],
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

      for (const { id, odigosVersion } of targetConnections) {
        const queryString = getVersionedQuery(query, odigosVersion, addNotification);
        if (queryString) {
          const key = `v${trimVersion(odigosVersion)}` as keyof VersionedRemoteFetch;

          if (!versionedProxyIDs[key]) versionedProxyIDs[key] = { queryString, proxyIDs: [] };
          versionedProxyIDs[key]!.proxyIDs.push(id);
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
    loading: loading || multiLoading,
    data: safeJsonParse<{ data?: ResT }>(cachedData?.remoteFetch, EMPTY_RESPONSE_SINGLE).data,
    dataMulti: cachedMultiData?.remoteMultiFetch ? cachedMultiData.remoteMultiFetch.map(mapMultiFetchResult<ResT>) : EMPTY_RESPONSE_MULTI<ResT>(),
  };
};
