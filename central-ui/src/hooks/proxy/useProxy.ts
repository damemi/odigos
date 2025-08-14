import { REMOTE_FETCH } from '@/graphql';
import { useProxyID } from './useProxyID';
import { StatusType } from '@odigos/ui-kit/types';
import { safeJsonParse } from '@odigos/ui-kit/functions';
import { type ApolloError, useLazyQuery } from '@apollo/client';
import { useInstrumentStore, useNotificationStore } from '@odigos/ui-kit/store';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyObj = Record<string, any>;

interface UseProxy<ResT, ReqT> {
  fetch: (variables?: ReqT) => Promise<{ data?: ResT }>;
  loading: boolean;
  error?: ApolloError;
  data?: ResT;
}

const EMPTY_RESPONSE = { data: undefined };

export const useProxy = <ResT = AnyObj, ReqT = AnyObj>(queryString: string): UseProxy<ResT, ReqT> => {
  const { proxyID } = useProxyID();
  const { addNotification } = useNotificationStore();
  const { setInstrumentAwait, setInstrumentCount } = useInstrumentStore();

  const [remoteFetch, { data: cachedData, loading }] = useLazyQuery(REMOTE_FETCH);

  const fetch: UseProxy<ResT, ReqT>['fetch'] = async (variables) => {
    if (!proxyID || !queryString) {
      // addNotification({ type: StatusType.Error, title: 'Request Error', message: 'Unable to load data from the proxy. Please make sure your connection is configured correctly.' });
      console.warn('no `proxyID` or `queryString` to load data from the proxy. Please make sure your connection is configured correctly.');

      return EMPTY_RESPONSE;
    }

    const { data, error } = await remoteFetch({
      variables: {
        proxyID,
        query: queryString,
        variables: variables || {},
      },
    });

    if (error) {
      addNotification({ type: StatusType.Error, title: error.name || 'API Error', message: error.cause?.message || error.message });

      // it does not matter where the error comes from, we should reset global stores
      setInstrumentAwait(false);
      setInstrumentCount('sourcesToCreate', 0);
      setInstrumentCount('sourcesCreated', 0);

      return EMPTY_RESPONSE;
    } else {
      return safeJsonParse(data?.remoteFetch, EMPTY_RESPONSE);
    }
  };

  return {
    fetch,
    loading,
    data: safeJsonParse(cachedData?.remoteFetch, EMPTY_RESPONSE).data,
  };
};
