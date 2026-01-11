import { useEffect } from 'react';
import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
import { useNamespace } from '../namespaces';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import type { SourceConditions, SourceInstrumentInput } from '@/types';
import { addAgentInjectionStatusToSources, addConditionToSources, prepareNamespacePayloads, prepareSourcePayloads } from '@/utils';
import { GET_SOURCE, GET_SOURCE_CONDITIONS, GET_SOURCE_LIBRARIES, GET_SOURCES, GET_WORKLOADS, PERSIST_SOURCES, UPDATE_SOURCE } from '@/graphql';
import { Crud, EntityTypes, StatusType, type SourceFormData, type Source, type WorkloadId, type InstrumentationInstanceComponent, Workload } from '@odigos/ui-kit/types';
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

interface UseSourceCrud {
  sources: Source[];
  sourcesLoading: boolean;
  fetchSources: () => Promise<void>;
  fetchSourceById: (id: WorkloadId, bypassPaginationLoader?: boolean) => Promise<Source | undefined>;
  fetchSourceLibraries: (id: WorkloadId) => Promise<{ data?: { instrumentationInstanceComponents: InstrumentationInstanceComponent[] } }>;
  persistSources: (selectAppsList: SourceSelectionFormData, futureSelectAppsList: NamespaceSelectionFormData) => Promise<void>;
  updateSource: (sourceId: WorkloadId, payload: SourceFormData) => Promise<void>;
}

export const useSourceCRUD = (): UseSourceCrud => {
  const { isReadonly } = useConfig();
  const { setProgress } = useProgressStore();
  const { persistNamespaces } = useNamespace();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { sourcesLoading, setEntitiesLoading, sources, setEntities, addEntities, removeEntities } = useEntityStore();
  const { setFetchedAllNamespaces, setAvailableSources, setConfiguredSources, setConfiguredFutureApps } = useSetupStore();

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

  const { fetch: queryAll } = useRemoteFetch<{ computePlatform?: { sources?: Source[] } }>(GET_SOURCES);
  const { fetch: queryById } = useRemoteFetch<{ computePlatform?: { source?: Source } }, { sourceId: WorkloadId }>(GET_SOURCE);
  const { fetch: mutatePersistSources } = useRemoteFetch<{ persistK8sSources: boolean }, SourceInstrumentInput>(PERSIST_SOURCES);
  const { fetch: mutateUpdate } = useRemoteFetch<{ updateK8sActualSource: boolean }, { sourceId: WorkloadId; patchSourceRequest: SourceFormData }>(UPDATE_SOURCE);
  const { fetch: queryOtherConditions } = useRemoteFetch<{ sourceConditions: SourceConditions[] }>(GET_SOURCE_CONDITIONS);
  const { fetch: querySourceLibraries } = useRemoteFetch<{ instrumentationInstanceComponents: InstrumentationInstanceComponent[] }, WorkloadId>(GET_SOURCE_LIBRARIES);
  const { fetch: queryWorkloads } = useRemoteFetch<{ workloads: Workload[] }, { filter?: WorkloadId }>(GET_WORKLOADS);

  const handleInstrumentationCount = (toAddCount: number, toDeleteCount: number) => {
    const { progress } = useProgressStore.getState();

    // TODO: estimate the number of instrumentationConfigs to create for future-apps

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

    const { data } = await queryWorkloads(reqPayload);
    const { workloads } = data || {};

    if (workloads) {
      const tempSources: Source[] = [];

      for (const item of workloads) {
        const updatedSource = addAgentInjectionStatusToSources(item, allSources);
        if (updatedSource) tempSources.push(updatedSource);
      }

      addEntities(EntityTypes.Source, tempSources);
    }
  };

  const fetchSources = async () => {
    setEntitiesLoading(EntityTypes.Source, true);

    const { data } = await queryAll();
    const { sources: fetchedSources } = data?.computePlatform || {};

    if (fetchedSources) {
      setEntities(EntityTypes.Source, fetchedSources);
      setEntitiesLoading(EntityTypes.Source, false);
      if (fetchedSources.length) {
        fetchAllAgentInjectionStatuses(fetchedSources);
        fetchAllConditions(fetchedSources);
      }
    }
  };

  const fetchSourceById = async (id: WorkloadId): Promise<Source | undefined> => {
    const { data } = await queryById({ sourceId: id });
    const { source } = data?.computePlatform || {};

    if (source) {
      addEntities(EntityTypes.Source, [source]);
      fetchAllAgentInjectionStatuses([source]);
      return source;
    }
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

      setFetchedAllNamespaces(false);
      setAvailableSources({});
      setConfiguredSources({});
      setConfiguredFutureApps({});

      // !! no "fetch" and no "setInstrumentAwait(false)"
      // !! we should wait for SSE to handle that
    }
  };

  const updateSource: UseSourceCrud['updateSource'] = async (sourceId, payload) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Updating source...', undefined, true);

      const { data } = await mutateUpdate({ sourceId, patchSourceRequest: payload });
      if (data?.updateK8sActualSource) notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${sourceId.name}" source`, sourceId);

      // !! no "fetch"
      // !! we should wait for SSE to handle that
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
    fetchSourceById,
    fetchSourceLibraries: querySourceLibraries,
    persistSources,
    updateSource,
  };
};
