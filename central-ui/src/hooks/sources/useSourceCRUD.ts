import { useEffect } from 'react';
import { useConfig } from '../config';
import { useProxy, useRemoteFetch } from '../proxy';
import { useNamespace } from '../namespaces';
import { getIdFromSseTarget, getSseTargetFromId, trimVersion } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import type { SourceConditions, SourceInstrumentInput, WorkloadResponse, WorkloadWithOdigosHealthStatus } from '@/types';
import { addAgentInjectionStatusToSources, addConditionToSources, mapWorkloadToSource, sortSources, prepareNamespacePayloads, prepareSourcePayloads } from '@/utils';
import { GET_PEER_SOURCES, GET_SOURCE, GET_SOURCE_CONDITIONS, GET_SOURCE_LIBRARIES, GET_SOURCES, GET_WORKLOADS, GET_WORKLOADS_BY_IDS, PERSIST_SOURCES, UPDATE_SOURCE } from '@/graphql';
import { Crud, EntityTypes, StatusType, type PeerSources, type SourceFormData, type Source, type WorkloadId, type InstrumentationInstanceComponent, type Workload } from '@odigos/ui-kit/types';
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
  updateSource: (sourceId: WorkloadId, payload: SourceFormData) => Promise<void>;
}

export const useSourceCRUD = (): UseSourceCrud => {
  const { odigosVersion } = useProxy();
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
  const isV120Plus = odigosVersion ? trimVersion(odigosVersion) >= V120 : false;

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

    const ids = targets
      .map((t) => getIdFromSseTarget(t, EntityTypes.Source) as WorkloadId)
      .filter((id) => id.namespace && id.name && id.kind);

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
    const { data: sourceData } = await queryById({ sourceId: id });
    const { source } = sourceData?.computePlatform || {};

    if (!source) return undefined;

    if (isV120Plus) {
      const { data: workloadData } = await queryWorkloads({ filter: { namespace: id.namespace, kind: id.kind, name: id.name } });
      const workload = workloadData?.workloads?.[0];

      if (workload) {
        const mapped = mapWorkloadToSource(workload);
        const enrichedSource: Source = {
          ...source,
          workloadOdigosHealthStatus: mapped.workloadOdigosHealthStatus,
          podsAgentInjectionStatus: mapped.podsAgentInjectionStatus,
          rollbackOccurred: mapped.rollbackOccurred,
        };
        addEntities(EntityTypes.Source, [enrichedSource]);
        return enrichedSource;
      }
    } else {
      // @deprecated-legacy-api(v1.20) - Remove this else branch
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

  const updateSource: UseSourceCrud['updateSource'] = async (sourceId, payload) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Updating source...', undefined, true);

      const { data } = await mutateUpdate({ sourceId, patchSourceRequest: payload });
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
    updateSource,
  };
};
