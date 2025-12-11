import { REMOTE_MULTI_FETCH } from '@/graphql';
import { StatusType } from '@odigos/ui-kit/types';
import { safeJsonParse } from '@odigos/ui-kit/functions';
import { type ApolloError, useLazyQuery } from '@apollo/client';
import { useNotificationStore } from '@odigos/ui-kit/store';
import { useCallback, useState } from 'react';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyObj = Record<string, any>;

export interface MultiProxyResult<T> {
  proxyID: string;
  success: boolean;
  data?: T;
  error?: string;
}

export interface MultiProxyResponse<T> {
  error?: string;
  results: MultiProxyResult<T>[];
  allSucceeded: boolean;
  anySucceeded: boolean;
  successCount: number;
  failureCount: number;
}

interface UseMultiProxy<ResT, ReqT> {
  fetchAll: (proxyIDs: string[], variables?: ReqT) => Promise<MultiProxyResponse<ResT>>;
  loading: boolean;
  error?: ApolloError;
}

const EMPTY_RESPONSE = (error?: string): MultiProxyResponse<unknown> => ({
  error,
  results: [],
  allSucceeded: false,
  anySucceeded: false,
  successCount: 0,
  failureCount: 0,
});

export const useMultiProxy = <ResT = AnyObj, ReqT = AnyObj>(queryString: string): UseMultiProxy<ResT, ReqT> => {
  const { addNotification } = useNotificationStore();
  const [loading, setLoading] = useState(false);

  const [remoteMultiFetch] = useLazyQuery(REMOTE_MULTI_FETCH);

  const fetchAll: UseMultiProxy<ResT, ReqT>['fetchAll'] = useCallback(
    async (proxyIDs, variables) => {
      if (!proxyIDs.length || !queryString) {
        console.warn('no `proxyIDs` or `queryString` provided for multi-proxy fetch');
        return EMPTY_RESPONSE() as MultiProxyResponse<ResT>;
      }

      setLoading(true);
      try {
        const { data, error } = await remoteMultiFetch({
          variables: {
            proxyIDs,
            query: queryString,
            variables: variables || {},
          },
        });

        if (error) {
          addNotification({ type: StatusType.Error, title: error.name || 'API Error', message: error.cause?.message || error.message });
          return EMPTY_RESPONSE(error.cause?.message || error.message) as MultiProxyResponse<ResT>;
        }

        const rawResults = data?.remoteMultiFetch || [];
        const results: MultiProxyResult<ResT>[] = rawResults.map((result: { proxyID: string; success: boolean; data?: string; error?: string }) => ({
          proxyID: result.proxyID,
          success: result.success,
          data: result.data ? safeJsonParse(result.data, { data: undefined }).data : undefined,
          error: result.error,
        }));

        const successCount = results.filter((r) => r.success).length;
        const failureCount = results.length - successCount;

        return {
          results,
          allSucceeded: failureCount === 0,
          anySucceeded: successCount > 0,
          successCount,
          failureCount,
        };
      } finally {
        setLoading(false);
      }
    },
    [queryString, remoteMultiFetch, addNotification],
  );

  return {
    fetchAll,
    loading,
  };
};
