import { useEffect } from 'react';
import { GET_METRICS } from '@/graphql';
import { useRemoteFetch } from '../proxy';
import type { Metrics } from '@odigos/ui-kit/types';
import { useDrawerStore, useEntityStore, useModalStore } from '@odigos/ui-kit/store';

export const useMetrics = () => {
  const { drawerType } = useDrawerStore();
  const { currentModal } = useModalStore();
  const sources = useEntityStore((state) => state.sources);
  const destinations = useEntityStore((state) => state.destinations);

  const { data, fetch } = useRemoteFetch<{ getOverviewMetrics?: Metrics }>(GET_METRICS);

  useEffect(() => {
    if (sources.length > 0 || destinations.length > 0 || !!drawerType || !!currentModal) {
      fetch();

      const interval = setInterval(fetch, 10000);
      return () => clearInterval(interval);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sources, destinations, drawerType, currentModal]);

  return {
    metrics: data?.getOverviewMetrics || {
      sources: [],
      destinations: [],
    },
  };
};
