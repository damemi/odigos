import { useEffect } from 'react';
import { useRemoteFetch } from '../proxy';
import { GET_DESTINATION_CATEGORIES } from '@/graphql';
import type { FetchedDestinationCategories } from '@/types';

/** Categories loaded eagerly on mount (for legacy destination edit drawer). */
export const useDestinationCategoriesLegacy = () => {
  const { called, data, fetch } = useRemoteFetch<FetchedDestinationCategories>(GET_DESTINATION_CATEGORIES);

  useEffect(() => {
    if (!called) fetch();
  }, [called, fetch]);

  return {
    categories: data?.destinationCategories?.categories || [],
  };
};
