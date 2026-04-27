import { useCallback } from 'react';
import { useProxyStore } from '@/store';
import { useRemoteFetch } from '../proxy';
import { pickFallbackProxyID } from '@/utils';
import { GET_DESTINATION_CATEGORIES } from '@/graphql';
import type { GetDestinationCategoriesResult } from '@odigos/ui-kit/types';
import type { ComputePlatform, FetchedDestinationCategories } from '@/types';

/**
 * @param connections Optional list of all known compute platforms. Used when
 * the hook is invoked from a page that doesn't have a selected proxy (e.g. the
 * central connections page) — we then fall back to the first successful K8s
 * connection, since destination categories are identical across clusters.
 */
export const useDestinationCategories = (connections: ComputePlatform[] = []) => {
  const { proxyID } = useProxyStore();
  const { fetch } = useRemoteFetch<FetchedDestinationCategories>(GET_DESTINATION_CATEGORIES, connections);

  const getDestinationCategories = useCallback(async (): Promise<GetDestinationCategoriesResult | undefined> => {
    const targetProxyID = proxyID || pickFallbackProxyID(connections);
    const { data } = await fetch(undefined, targetProxyID);
    return data as GetDestinationCategoriesResult | undefined;
  }, [fetch, proxyID, connections]);

  return {
    getDestinationCategories,
  };
};
