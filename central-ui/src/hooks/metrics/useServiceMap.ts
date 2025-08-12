import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useSourceCRUD } from '../sources';
import { GET_SERVICE_MAP } from '@/graphql';
import type { ServiceMapSources } from '@odigos/ui-kit/types';

export const useServiceMap = () => {
  const { sources } = useSourceCRUD();

  const { data, fetch } = useProxy<{ getServiceMap: { services: ServiceMapSources } }>(GET_SERVICE_MAP);

  useEffect(() => {
    if (sources.length > 0) {
      fetch();

      const interval = setInterval(fetch, 3000);
      return () => clearInterval(interval);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sources]);

  return {
    serviceMap: data?.getServiceMap?.services || [],
  };
};
