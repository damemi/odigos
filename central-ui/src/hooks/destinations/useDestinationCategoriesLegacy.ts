import { useEffect } from 'react';
import { useProxyStore } from '@/store';
import { useRemoteFetch } from '../proxy';
import { pickFallbackProxyID } from '@/utils';
import { GET_DESTINATION_CATEGORIES } from '@/graphql';
import type { ComputePlatform, FetchedDestinationCategories } from '@/types';

/**
 * Categories loaded eagerly on mount (for legacy destination edit drawer).
 *
 * @param connections Optional list of all known compute platforms — used to
 * fall back to the first successful K8s connection when no proxy is selected
 * (categories are identical across clusters).
 */
export const useDestinationCategoriesLegacy = (connections: ComputePlatform[] = []) => {
  const { proxyID } = useProxyStore();
  const { called, data, fetch } = useRemoteFetch<FetchedDestinationCategories>(GET_DESTINATION_CATEGORIES, connections);

  useEffect(() => {
    if (called) return;
    const targetProxyID = proxyID || pickFallbackProxyID(connections);
    if (targetProxyID) fetch(undefined, targetProxyID);
  }, [called, fetch, proxyID, connections]);

  return {
    categories: data?.destinationCategories?.categories || [],
  };
};
