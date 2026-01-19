import { useEffect } from 'react';
import { useRemoteFetch } from '../proxy';
import { GET_DESTINATION_CATEGORIES } from '@/graphql';
import type { FetchedDestinationCategories } from '@/types';

export const useDestinationCategories = () => {
  const { called, data, fetch } = useRemoteFetch<FetchedDestinationCategories>(GET_DESTINATION_CATEGORIES);

  useEffect(() => {
    if (!called) fetch();
  }, [called]);

  return {
    categories: data?.destinationCategories?.categories || [],
  };
};
