import { useEffect } from 'react';
import { useRemoteFetch } from '../proxy';
import { useSourceCRUD } from '../sources';
import { GET_SERVICE_MAP } from '@/graphql';
import type { ServiceMapSources } from '@odigos/ui-kit/types';

export const useServiceMap = () => {
  const { sources } = useSourceCRUD();

  const { data, fetch } = useRemoteFetch<{ getServiceMap: { services: ServiceMapSources } }>(GET_SERVICE_MAP);

  useEffect(() => {
    if (sources.length > 0) {
      fetch();

      const interval = setInterval(fetch, 15000);
      return () => clearInterval(interval);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sources]);

  return {
    serviceMap: data?.getServiceMap?.services || [],
  };
};
