import { useEffect } from 'react';
import { GET_METRICS } from '@/graphql';
import { useRemoteFetch } from '../proxy';
import type { Metrics } from '@odigos/ui-kit/types';
import { useEntityStore } from '@odigos/ui-kit/store';

export const useMetrics = () => {
  const sources = useEntityStore((state) => state.sources);
  const destinations = useEntityStore((state) => state.destinations);

  const { data, fetch } = useRemoteFetch<{ getOverviewMetrics?: Metrics }>(GET_METRICS);

  useEffect(() => {
    if (sources.length > 0 || destinations.length > 0) {
      fetch();

      const interval = setInterval(fetch, 10000);
      return () => clearInterval(interval);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sources, destinations]);

  return {
    metrics: data?.getOverviewMetrics || {
      sources: [],
      destinations: [],
    },
  };
};
