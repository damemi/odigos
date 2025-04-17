import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import { useNamespace } from '../namespaces';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import type { InstrumentationInstancesHealth, PaginatedData, SourceUpdateInput } from '@/types';
import { addConditionToSources, prepareNamespacePayloads, prepareSourcePayloads } from '@/utils';
import { GET_INSTRUMENTATION_INSTANCES_HEALTH, GET_SOURCE, GET_SOURCES, PERSIST_SOURCES, UPDATE_SOURCE } from '@/graphql';
import { Crud, EntityTypes, StatusType, type SourceFormData, type Source, type WorkloadId } from '@odigos/ui-kit/types';
import { type NamespaceSelectionFormData, type SourceSelectionFormData, useEntityStore, useInstrumentStore, useNotificationStore, usePendingStore, useSetupStore } from '@odigos/ui-kit/store';

interface UseSourceCrud {
  sources: Source[];
  sourcesLoading: boolean;
  fetchSourcesPaginated: (getAll?: boolean, nextPage?: string) => Promise<void>;
  fetchSourceById: (id: WorkloadId, bypassPaginationLoader?: boolean) => Promise<void>;
  persistSources: (selectAppsList: SourceSelectionFormData, futureSelectAppsList: NamespaceSelectionFormData) => Promise<void>;
  updateSource: (sourceId: WorkloadId, payload: SourceFormData) => Promise<void>;
}

export const useSourceCRUD = (): UseSourceCrud => {
  const { isReadonly } = useConfig();
  const { persistNamespace } = useNamespace();
  const { addNotification } = useNotificationStore();
  const { addPendingItems, removePendingItems } = usePendingStore();
  const { setInstrumentAwait, setInstrumentCount } = useInstrumentStore();
  const { setConfiguredSources, setConfiguredFutureApps } = useSetupStore();
  const { sourcesLoading, setEntitiesLoading, sources, addEntities, removeEntities } = useEntityStore();

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

  const { fetch: queryByPage } = useProxy<{ computePlatform?: { sources?: PaginatedData<Source> } }>(GET_SOURCES);
  const { fetch: queryById } = useProxy<{ computePlatform?: { source?: Source } }>(GET_SOURCE);
  const { fetch: mutatePersistSources } = useProxy<{ persistK8sSources: boolean }>(PERSIST_SOURCES);
  const { fetch: mutateUpdate } = useProxy<{ updateK8sActualSource: boolean }>(UPDATE_SOURCE);
  const { fetch: queryInstances } = useProxy<{ instrumentationInstancesHealth: InstrumentationInstancesHealth[] }>(GET_INSTRUMENTATION_INSTANCES_HEALTH);

  const shouldFetchSource = (allowFetchDuringLoadTrue?: boolean) => {
    // We should not fetch if we are already fetching.
    const { sourcesLoading } = useEntityStore.getState();
    // We should not fetch while sources are being instrumented.
    const { isAwaitingInstrumentation } = useInstrumentStore.getState();

    return !isAwaitingInstrumentation && (!sourcesLoading || (sourcesLoading && allowFetchDuringLoadTrue));
  };

  const handleInstrumentationCount = (toAddCount: number, toDeleteCount: number) => {
    const { sourcesToCreate, sourcesToDelete } = useInstrumentStore.getState();

    setInstrumentCount('sourcesToDelete', sourcesToDelete + toDeleteCount);
    setInstrumentCount('sourcesToCreate', sourcesToCreate + toAddCount);
  };

  const fetchAllInstances = async () => {
    const sourcesFromStore = useEntityStore.getState().sources;
    const { data } = await queryInstances();

    if (data?.instrumentationInstancesHealth) {
      const sourcesWithInstances: Source[] = [];

      for (const instanceHealth of data.instrumentationInstancesHealth) {
        const updatedSource = addConditionToSources(instanceHealth, sourcesFromStore);
        if (updatedSource) sourcesWithInstances.push(updatedSource);
      }

      addEntities(EntityTypes.Source, sourcesWithInstances);
    }
  };

  const fetchSourcesPaginated = async (getAll: boolean = true, page: string = '') => {
    if (!shouldFetchSource(!!page)) return;
    setEntitiesLoading(EntityTypes.Source, true);

    const { data } = await queryByPage({ nextPage: page });

    if (data?.computePlatform?.sources) {
      const { items, nextPage } = data.computePlatform.sources;

      addEntities(EntityTypes.Source, items);

      if (getAll && nextPage) {
        fetchSourcesPaginated(true, nextPage);
      } else if (useEntityStore.getState().sources.length >= useInstrumentStore.getState().sourcesToCreate) {
        setEntitiesLoading(EntityTypes.Source, false);
        setInstrumentCount('sourcesToCreate', 0);
        setInstrumentCount('sourcesCreated', 0);
        fetchAllInstances();
      }
    }
  };

  const fetchSourceById = async (id: WorkloadId, bypassPaginationLoader: boolean = false) => {
    if (!shouldFetchSource(bypassPaginationLoader)) return;

    const { data } = await queryById({ sourceId: id });

    if (data?.computePlatform?.source) {
      addEntities(EntityTypes.Source, [data.computePlatform.source]);
    }
  };

  const persistSources: UseSourceCrud['persistSources'] = async (selectAppsList, futureSelectAppsList) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      let alreadyNotified = false;
      const { payloads: persistSourcesPayloads, isEmpty: sourcesEmpty } = prepareSourcePayloads(selectAppsList, handleInstrumentationCount, removeEntities);
      const { payloads: persistNamespacesPayloads, isEmpty: futueAppsEmpty } = prepareNamespacePayloads(futureSelectAppsList);

      if (!sourcesEmpty && !alreadyNotified) {
        alreadyNotified = true;
        notifyUser(StatusType.Default, 'Pending', 'Persisting sources...', undefined, true);
        setInstrumentAwait(true);
      }
      if (!futueAppsEmpty && !alreadyNotified) {
        alreadyNotified = true;
        notifyUser(StatusType.Default, 'Pending', 'Persisting namespaces...', undefined, true);
        // TODO: estimate the number of instrumentationConfigs to create for future apps in "handleInstrumentationCount", then uncomment the below
        // setInstrumentAwait(true);
      }

      await Promise.all(persistSourcesPayloads.map(mutatePersistSources));
      setConfiguredSources({});
      await Promise.all(persistNamespacesPayloads.map(persistNamespace));
      setConfiguredFutureApps({});

      // !! no "fetch" and no "setInstrumentAwait(false)""
      // !! we should wait for SSE to handle that
      // TODO: SSE for centralized
    }
  };

  const updateSource: UseSourceCrud['updateSource'] = async (sourceId, payload) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      notifyUser(StatusType.Default, 'Pending', 'Updating source...', undefined, true);
      addPendingItems([{ entityType: EntityTypes.Source, entityId: sourceId }]);

      const patchSourceRequest: SourceUpdateInput = payload;
      await mutateUpdate({ sourceId, patchSourceRequest });

      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${sourceId.name}" source`, sourceId);
      removePendingItems([{ entityType: EntityTypes.Source, entityId: sourceId }]);

      // !! no "fetch"
      // !! we should wait for SSE to handle that
      // TODO: SSE for centralized
    }
  };

  useEffect(() => {
    if (!sources.length && !sourcesLoading) fetchSourcesPaginated();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    sources,
    sourcesLoading,
    fetchSourcesPaginated,
    fetchSourceById,
    persistSources,
    updateSource,
  };
};
