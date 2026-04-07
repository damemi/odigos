import { useCallback } from 'react';
import { useRemoteFetch } from '../proxy';
import { GET_DESTINATION_CATEGORIES } from '@/graphql';
import type { FetchedDestinationCategories } from '@/types';
import type { GetDestinationCategoriesResult } from '@odigos/ui-kit/types';

export const useDestinationCategories = () => {
  const { fetch } = useRemoteFetch<FetchedDestinationCategories>(GET_DESTINATION_CATEGORIES);

  const getDestinationCategories = useCallback(async (): Promise<GetDestinationCategoriesResult | undefined> => {
    const { data } = await fetch();
    return data as GetDestinationCategoriesResult | undefined;
  }, [fetch]);

  return {
    getDestinationCategories,
  };
};
