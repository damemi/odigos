import { useCallback } from 'react';
import { useRemoteFetch } from '../proxy';
import type { WorkloadId } from '@odigos/ui-kit/types';
import { ENABLE_SOURCE_PROFILING, GET_PROFILING_SLOTS, GET_SOURCE_PROFILING } from '@/graphql';

interface ProfilingSlots {
  activeKeys: string[];
  keysWithData: string[];
  totalBytesUsed: number;
  slotMaxBytes: number;
  maxSlots: number;
  maxTotalBytesBudget: number;
  slotTtlSeconds: number;
}

interface EnableProfilingResult {
  status: string;
  sourceKey: string;
  maxSlots: number;
  activeSlots: number;
}

interface SourceProfilingResult {
  profileJson: string;
}

interface UseProfiling {
  enableProfiling: (source: WorkloadId) => Promise<EnableProfilingResult | undefined>;
  fetchProfilingSlots: () => Promise<ProfilingSlots | undefined>;
  fetchSourceProfiling: (source: WorkloadId) => Promise<SourceProfilingResult | undefined>;
}

export const useProfiling = (): UseProfiling => {
  const { fetch: mutateEnable } = useRemoteFetch<{ enableSourceProfiling: EnableProfilingResult }, WorkloadId>(ENABLE_SOURCE_PROFILING);
  const { fetch: querySlots } = useRemoteFetch<{ profilingSlots: ProfilingSlots }>(GET_PROFILING_SLOTS);
  const { fetch: querySourceProfiling } = useRemoteFetch<{ computePlatform?: { source?: { profiling: SourceProfilingResult } } }, { sourceId: WorkloadId }>(GET_SOURCE_PROFILING);

  // Returns buffer/slot diagnostics: which workloads have active slots, which have buffered data, and memory usage.
  // Example response: { activeKeys: ["default/Deployment/inventory", ...], keysWithData: [...], totalBytesUsed: 4897024, ... }
  const fetchProfilingSlots: UseProfiling['fetchProfilingSlots'] = useCallback(async () => {
    const { data } = await querySlots();
    return data?.profilingSlots;
  }, [querySlots]);

  // Activates (or refreshes) a profiling slot for a workload. Must be called before fetchSourceProfiling will return data.
  // Example: await enableProfiling({ namespace: "default", kind: "Deployment", name: "inventory" })
  //       => { status: "ok", sourceKey: "default/Deployment/inventory", maxSlots: 24, activeSlots: 6 }
  const enableProfiling: UseProfiling['enableProfiling'] = useCallback(
    async (source) => {
      const { data } = await mutateEnable(source);
      return data?.enableSourceProfiling;
    },
    [mutateEnable],
  );

  // Fetches the aggregated Pyroscope-shaped flame graph for a workload. Returns a JSON-encoded FlamebearerProfile.
  // Example: await fetchSourceProfiling({ namespace: "default", kind: "Deployment", name: "inventory" })
  //       => { profileJson: '{"version":1,"flamebearer":{"names":[...],"levels":[...],...},...}' }
  const fetchSourceProfiling: UseProfiling['fetchSourceProfiling'] = useCallback(
    async (source) => {
      const { data } = await querySourceProfiling({ sourceId: source });
      return data?.computePlatform?.source?.profiling;
    },
    [querySourceProfiling],
  );

  return {
    fetchProfilingSlots,
    enableProfiling,
    fetchSourceProfiling,
  };
};
