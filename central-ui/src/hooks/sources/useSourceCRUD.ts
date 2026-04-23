import { useEffect } from 'react';
import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
import { useNamespace } from '../namespaces';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { getIdFromSseTarget, getSseTargetFromId, trimVersion } from '@odigos/ui-kit/functions';
import type { NamespaceInstrumentInput, SourceConditions, SourceInstrumentInput, WorkloadResponse, WorkloadWithOdigosHealthStatus } from '@/types';
import { addAgentInjectionStatusToSources, addConditionToSources, mapWorkloadToSource, sortSources, prepareNamespacePayloads, prepareSourcePayloads } from '@/utils';
import { GET_PEER_SOURCES, GET_SOURCE, GET_SOURCE_CONDITIONS, GET_SOURCE_LIBRARIES, GET_SOURCES, GET_WORKLOADS, GET_WORKLOADS_BY_IDS, PERSIST_SOURCES, UPDATE_SOURCE } from '@/graphql';
import {
  Crud,
  EntityTypes,
  StatusType,
  type PeerSources,
  type PersistSourceInput,
  type SourceFormData,
  type Source,
  type WorkloadId,
  type InstrumentationInstanceComponent,
  type Workload,
} from '@odigos/ui-kit/types';
import {
  type NamespaceSelectionFormData,
  type SourceSelectionFormData,
  useDataStreamStore,
  useEntityStore,
  useNotificationStore,
  useSetupStore,
  ProgressKeys,
  useProgressStore,
} from '@odigos/ui-kit/store';

const V120 = trimVersion('v1.20');
const MAX_INDIVIDUAL_FETCH = 50;

interface UseSourceCrud {
  sources: Source[];
  sourcesLoading: boolean;
  fetchSources: () => Promise<void>;
  fetchSourcesByTargets: (targets: string[]) => Promise<void>;
  fetchSourceById: (id: WorkloadId, bypassPaginationLoader?: boolean) => Promise<Source | undefined>;
  fetchSourceLibraries: (id: WorkloadId) => Promise<{ data?: { instrumentationInstanceComponents: InstrumentationInstanceComponent[] } }>;
  fetchPeerSources: (serviceName: string) => Promise<{ data?: { peerSources: PeerSources } }>;
  persistSources: (selectAppsList: SourceSelectionFormData, futureSelectAppsList: NamespaceSelectionFormData) => Promise<void>;
  persistSourcesV2: (payload: PersistSourceInput) => Promise<{ error?: string } | undefined>;
  updateSource: (sourceId: WorkloadId, payload: SourceFormData) => Promise<void>;
}

export const useSourceCRUD = (): UseSourceCrud => {
  const { version, isVm } = useOdigos();
  const { isReadonly } = useConfig();
  const { setProgress } = useProgressStore();
  const { persistNamespaces } = useNamespace();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { setConfiguredSources, setConfiguredFutureApps } = useSetupStore();
  const { sourcesLoading, setEntitiesLoading, sources, setEntities, addEntities, removeEntities } = useEntityStore();

  // @deprecated-legacy-api(v1.20) - Remove when min supported proxy >= v1.20
  // The isV120Plus check, legacy query hooks (queryAllSources, queryOtherConditions,
  // queryLegacyWorkloads), and helper functions (fetchAllConditions, fetchAllAgentInjectionStatuses)
  // are only needed for proxies < v1.20. When removed, keep only the v1.20+ path in each function.
  const isV120Plus = trimVersion(version) >= V120;

  const notifyUser = (type: StatusType, title: string, message: string, id?: WorkloadId, hideFromHistory?: boolean) => {
    addNotification({
      type,
      title,
      message,
      crdType: EntityTypes.Source,
      target: id ? getSseTargetFromId(id, EntityTypes.Source) : undefined,
      hideFromHistory,
    });
  };

  // v1.20+ queries
  const { fetch: queryWorkloads } = useRemoteFetch<{ workloads: WorkloadResponse[] }, { filter?: { markedForInstrumentation?: boolean } & Partial<WorkloadId> }>(GET_WORKLOADS);
  const { fetch: queryWorkloadsByIds } = useRemoteFetch<{ workloadsByIds: WorkloadResponse[] }, { ids: { namespace: string; kind: string; name: string }[] }>(GET_WORKLOADS_BY_IDS);

  // legacy queries (< v1.20)
  const { fetch: queryAllSources } = useRemoteFetch<{ computePlatform?: { sources?: Source[] } }>(GET_SOURCES);
  const { fetch: queryOtherConditions } = useRemoteFetch<{ sourceConditions: SourceConditions[] }>(GET_SOURCE_CONDITIONS);
  const { fetch: queryLegacyWorkloads } = useRemoteFetch<{ workloads: Workload[] }, { filter?: WorkloadId }>(GET_WORKLOADS);

  // shared queries
  const { fetch: queryById } = useRemoteFetch<{ computePlatform?: { source?: Source } }, { sourceId: WorkloadId }>(GET_SOURCE);
  const { fetch: mutatePersistSources } = useRemoteFetch<{ persistK8sSources: boolean }, SourceInstrumentInput>(PERSIST_SOURCES);
  const { fetch: mutateUpdate } = useRemoteFetch<{ updateK8sActualSource: boolean }, { sourceId: WorkloadId; patchSourceRequest: SourceFormData }>(UPDATE_SOURCE);
  const { fetch: querySourceLibraries } = useRemoteFetch<{ instrumentationInstanceComponents: InstrumentationInstanceComponent[] }, WorkloadId>(GET_SOURCE_LIBRARIES);
  const { fetch: queryPeerSources } = useRemoteFetch<{ peerSources: PeerSources }, { serviceName: string }>(GET_PEER_SOURCES);

  const handleInstrumentationCount = (toAddCount: number, toDeleteCount: number) => {
    const { progress } = useProgressStore.getState();

    if (toAddCount > 0)
      setProgress(ProgressKeys.Instrumenting, {
        total: (progress[ProgressKeys.Instrumenting]?.total || 0) + toAddCount,
        current: progress[ProgressKeys.Instrumenting]?.current || 0,
      });
    if (toDeleteCount > 0)
      setProgress(ProgressKeys.Uninstrumenting, {
        total: (progress[ProgressKeys.Uninstrumenting]?.total || 0) + toDeleteCount,
        current: progress[ProgressKeys.Uninstrumenting]?.current || 0,
      });
  };

  // Legacy helpers for < v1.20 - enrich sources with conditions and injection status
  const fetchAllConditions = async (allSources: Source[]) => {
    // we do this to prevent unwanted "toast errors" in the UI
    if (isVm) return;

    const { data } = await queryOtherConditions();
    const { sourceConditions } = data || {};
    if (sourceConditions) {
      const tempSources: Source[] = [];
      for (const item of sourceConditions) {
        const updatedSource = addConditionToSources(item, allSources);
        if (updatedSource) tempSources.push(updatedSource);
      }
      addEntities(EntityTypes.Source, tempSources);
    }
  };

  const fetchAllAgentInjectionStatuses = async (allSources: Source[]) => {
    // we do this to prevent unwanted "toast errors" in the UI
    if (isVm) return;

    const reqPayload = allSources.length === 1 ? { filter: { namespace: allSources[0].namespace, kind: allSources[0].kind, name: allSources[0].name } } : undefined;
    const { data } = await queryLegacyWorkloads(reqPayload);
    const { workloads } = data || {};
    if (workloads) {
      const tempSources: Source[] = [];
      for (const item of workloads) {
        const updatedSource = addAgentInjectionStatusToSources(item as WorkloadWithOdigosHealthStatus, allSources);
        if (updatedSource) tempSources.push(updatedSource);
      }
      addEntities(EntityTypes.Source, tempSources);
    }
  };

  const fetchSources = async () => {
    setEntitiesLoading(EntityTypes.Source, true);

    if (isV120Plus) {
      const { data } = await queryWorkloads({ filter: { markedForInstrumentation: true } });
      if (data?.workloads) {
        const mappedSources = sortSources(data.workloads.map(mapWorkloadToSource));
        setEntities(EntityTypes.Source, mappedSources);
      }
    } else {
      // @deprecated-legacy-api(v1.20) - Remove this else branch
      const { data } = await queryAllSources();
      const { sources: fetchedSources } = data?.computePlatform || {};
      if (fetchedSources) {
        setEntities(EntityTypes.Source, fetchedSources);
        if (fetchedSources.length) {
          fetchAllAgentInjectionStatuses(fetchedSources);
          fetchAllConditions(fetchedSources);
        }
      }
    }

    setEntitiesLoading(EntityTypes.Source, false);
  };

  const fetchSourcesByTargets = async (targets: string[]) => {
    // @deprecated-legacy-api(v1.20) - Remove this early-return branch
    if (!isV120Plus) {
      await fetchSources();
      return;
    }

    const ids = targets.map((t) => getIdFromSseTarget(t, EntityTypes.Source) as WorkloadId).filter((id) => id.namespace && id.name && id.kind);

    if (ids.length === 0) return;

    if (ids.length > MAX_INDIVIDUAL_FETCH) {
      await fetchSources();
      return;
    }

    const { data } = await queryWorkloadsByIds({ ids: ids.map(({ namespace, kind, name }) => ({ namespace, kind, name })) });

    if (data?.workloadsByIds) {
      addEntities(EntityTypes.Source, data.workloadsByIds.map(mapWorkloadToSource));
    }
  };

  const fetchSourceById = async (id: WorkloadId): Promise<Source | undefined> => {
    if (isV120Plus) {
      // Aligned with odigos standalone (commit 710ac114d): single round-trip via queryWorkloadsByIds
      // returns the full workload payload (containers, conditions, agent status, rollback) plus
      // the base source identity — no need for the separate GET_SOURCE + GET_WORKLOADS calls.
      // Fall back to GET_SOURCE only if the workloads-by-ids path yields nothing (e.g. the source
      // exists but has no workload metadata yet).
      const { data } = await queryWorkloadsByIds({ ids: [{ namespace: id.namespace, kind: id.kind, name: id.name }] });
      const workload = data?.workloadsByIds?.[0];

      if (workload) {
        const mapped = mapWorkloadToSource(workload);
        addEntities(EntityTypes.Source, [mapped]);
        return mapped;
      }
    }

    // Legacy path (< v1.20) and v1.20+ fallback when workloadsByIds returns empty
    const { data: sourceData } = await queryById({ sourceId: id });
    const { source } = sourceData?.computePlatform || {};
    if (!source) return undefined;

    if (!isV120Plus) {
      // @deprecated-legacy-api(v1.20) - Remove this branch when min supported proxy >= v1.20
      addEntities(EntityTypes.Source, [source]);
      await fetchAllAgentInjectionStatuses([source]);
      const { sources: updatedSources } = useEntityStore.getState();
      return updatedSources.find((s) => s.namespace === id.namespace && s.name === id.name && s.kind === id.kind) || source;
    }

    addEntities(EntityTypes.Source, [source]);
    return source;
  };

  const persistSources: UseSourceCrud['persistSources'] = async (selectAppsList, futureSelectAppsList) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      let alreadyNotified = false;
      const { payload: persistSourcesPayloads, isEmpty: sourcesEmpty } = prepareSourcePayloads(selectAppsList, sources, selectedStreamName, handleInstrumentationCount, removeEntities, addEntities);
      const { payload: persistNamespacesPayloads, isEmpty: futueAppsEmpty } = prepareNamespacePayloads(futureSelectAppsList, selectedStreamName);

      if (!sourcesEmpty && !alreadyNotified) {
        alreadyNotified = true;
        notifyUser(StatusType.Default, 'Pending', 'Persisting sources...', undefined, true);
      }
      if (!futueAppsEmpty && !alreadyNotified) {
        alreadyNotified = true;
        notifyUser(StatusType.Default, 'Pending', 'Persisting namespaces...', undefined, true);
      }

      await mutatePersistSources(persistSourcesPayloads);
      await persistNamespaces(persistNamespacesPayloads);

      setConfiguredSources({});
      setConfiguredFutureApps({});
    }
  };

  const persistSourcesV2: UseSourceCrud['persistSourcesV2'] = async (input) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    }

    const entries = Object.entries(input);
    const errors: string[] = [];

    for await (const [_, inputs] of entries) {
      const workloadSources: SourceInstrumentInput = { sources: [] };
      const namespaceSources: NamespaceInstrumentInput = { namespaces: [] };

      for (const source of inputs) {
        if (source.name && source.kind) {
          workloadSources.sources.push({
            namespace: source.namespace,
            name: source.name,
            kind: source.kind,
            selected: source.selected,
            currentStreamName: source.currentStreamName,
          });
        } else {
          namespaceSources.namespaces.push({
            namespace: source.namespace,
            selected: source.selected,
            currentStreamName: source.currentStreamName,
          });
        }
      }

      if (namespaceSources.namespaces.length > 0) await persistNamespaces(namespaceSources);
      if (workloadSources.sources.length > 0) await mutatePersistSources(workloadSources);
    }

    return {
      error: errors.length > 0 ? `Failed to persist sources in ${errors.length} clusters: ${errors.join(', ')}` : undefined,
    };
  };

  const updateSource: UseSourceCrud['updateSource'] = async (sourceId, payload) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Updating source...', undefined, true);

      const { data } = await mutateUpdate({
        sourceId,
        patchSourceRequest: { ...payload, currentStreamName: selectedStreamName },
      });

      if (data?.updateK8sActualSource) notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${sourceId.name}" source`, sourceId);
    }
  };

  useEffect(() => {
    if (!sources.length && !useEntityStore.getState().sourcesLoading) fetchSources();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    sources,
    sourcesLoading,
    fetchSources,
    fetchSourcesByTargets,
    fetchSourceById,
    fetchSourceLibraries: querySourceLibraries,
    fetchPeerSources: (serviceName: string) => queryPeerSources({ serviceName }),
    persistSources,
    persistSourcesV2,
    updateSource,
  };
};
