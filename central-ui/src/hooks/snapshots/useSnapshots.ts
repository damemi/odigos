import { useCallback } from 'react';
import { useLazyQuery } from '@apollo/client';
import { useRemoteFetch } from '../proxy/useRemoteFetch';
import { ProgressKeys, useProgressStore } from '@odigos/ui-kit/store';
import type { ComputePlatform, NamespaceInstrumentInput, SourceInstrumentInput } from '@/types';
import type { AllClusterSnapshots, ClusterSnapshot, PersistSourceInput } from '@odigos/ui-kit/types';
import { GET_ALL_CLUSTER_SNAPSHOTS, GET_CLUSTER_SNAPSHOT, PERSIST_NAMESPACES, PERSIST_SOURCES } from '@/graphql';

export const useSnapshots = (connections: ComputePlatform[]) => {
  const [getAllClusterSnapshots] = useLazyQuery<{ allClusterSnapshots?: AllClusterSnapshots }>(GET_ALL_CLUSTER_SNAPSHOTS);
  const [getClusterSnapshot] = useLazyQuery<{ clusterSnapshot?: ClusterSnapshot }, { clusterId: string }>(GET_CLUSTER_SNAPSHOT);

  const { fetch: persistWorkloads } = useRemoteFetch<{ persistK8sSources: boolean }, SourceInstrumentInput>(PERSIST_SOURCES, connections);
  const { fetch: persistNamespaces } = useRemoteFetch<{ persistK8sNamespaces: boolean }, NamespaceInstrumentInput>(PERSIST_NAMESPACES, connections);

  const bulkPersistSources = useCallback(
    async (input: PersistSourceInput) => {
      const entries = Object.entries(input);
      const errors: string[] = [];

      const { setProgress, addProgress } = useProgressStore.getState();
      setProgress(ProgressKeys.BulkInstrumenting, { total: entries.length, current: 0 });

      for await (const [clusterId, sources] of entries) {
        const workloadSources: SourceInstrumentInput = { sources: [] };
        const namespaceSources: NamespaceInstrumentInput = { namespaces: [] };

        for (const source of sources) {
          if (source.name && source.kind) {
            // workload source
            workloadSources.sources.push({
              namespace: source.namespace,
              name: source.name,
              kind: source.kind,
              selected: source.selected,
              currentStreamName: source.currentStreamName,
            });
          } else {
            // namespace source
            namespaceSources.namespaces.push({
              namespace: source.namespace,
              selected: source.selected,
              currentStreamName: source.currentStreamName,
            });
          }
        }

        if (namespaceSources.namespaces.length > 0) {
          const namespacesResult = await persistNamespaces(namespaceSources, clusterId);
          if (!namespacesResult.data?.persistK8sNamespaces) errors.push(namespacesResult.error || 'Unknown error');
        }
        if (workloadSources.sources.length > 0) {
          const workloadsResult = await persistWorkloads(workloadSources, clusterId);
          if (!workloadsResult.data?.persistK8sSources) errors.push(workloadsResult.error || 'Unknown error');
        }

        addProgress(ProgressKeys.BulkInstrumenting, 1);
      }

      if (errors.length > 0) {
        return Promise.resolve({ error: `Failed to persist sources in ${errors.length} clusters: ${errors.join(', ')}` });
      }
      return Promise.resolve({ error: undefined });
    },
    [persistWorkloads, persistNamespaces],
  );

  return {
    getAllClusterSnapshots: () => getAllClusterSnapshots().then((res) => res?.data?.allClusterSnapshots),
    getClusterSnapshot: () => getClusterSnapshot().then((res) => res?.data?.clusterSnapshot),
    bulkPersistSources,
  };
};
