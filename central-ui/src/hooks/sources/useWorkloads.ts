import { useEffect, useMemo, useState } from 'react';
import { useRemoteFetch } from '../proxy';
import { GET_WORKLOADS } from '@/graphql';
import type { K8sResourceKind, Workload } from '@odigos/ui-kit/types';

interface WorkloadFilter {
  namespace?: string;
  kind?: K8sResourceKind;
  name?: string;
  markedForInstrumentation?: boolean;
}

/**
 * Fetches workloads from the currently selected proxy via useRemoteFetch.
 * Mirrors the odigos standalone useWorkloads public API.
 */
export const useWorkloads = (filter?: WorkloadFilter) => {
  const { fetch, called } = useRemoteFetch<{ workloads: Workload[] }, { filter?: WorkloadFilter }>(GET_WORKLOADS);
  const [workloads, setWorkloads] = useState<Workload[]>([]);
  const [loading, setLoading] = useState(false);

  const filterKey = useMemo(() => JSON.stringify(filter ?? {}), [filter]);

  useEffect(() => {
    let cancelled = false;
    const run = async () => {
      setLoading(true);
      const { data } = await fetch({ filter });
      if (cancelled) return;
      setWorkloads(data?.workloads ?? []);
      setLoading(false);
    };
    run();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filterKey, called]);

  return {
    workloads,
    loading,
  };
};
