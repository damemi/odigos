import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import { useNamespace } from '../namespaces';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import type { SourceConditions, SourceInstrumentInput } from '@/types';
import { addConditionToSources, prepareNamespacePayloads, prepareSourcePayloads } from '@/utils';
import { GET_SOURCE, GET_SOURCE_CONDITIONS, GET_SOURCE_LIBRARIES, GET_SOURCES, PERSIST_SOURCES, UPDATE_SOURCE } from '@/graphql';
import { Crud, EntityTypes, StatusType, type SourceFormData, type Source, type WorkloadId, type InstrumentationInstanceComponent } from '@odigos/ui-kit/types';
import {
  type NamespaceSelectionFormData,
  type SourceSelectionFormData,
  useDataStreamStore,
  useEntityStore,
  useInstrumentStore,
  useNotificationStore,
  usePendingStore,
  useSetupStore,
} from '@odigos/ui-kit/store';

interface UseSourceCrud {
  sources: Source[];
  sourcesLoading: boolean;
  fetchSources: () => Promise<void>;
  fetchSourceById: (id: WorkloadId, bypassPaginationLoader?: boolean) => Promise<Source | undefined>;
  fetchSourceLibraries: (req: { variables: WorkloadId }) => Promise<{ data?: { instrumentationInstanceComponents: InstrumentationInstanceComponent[] } }>;
  persistSources: (selectAppsList: SourceSelectionFormData, futureSelectAppsList: NamespaceSelectionFormData) => Promise<void>;
  updateSource: (sourceId: WorkloadId, payload: SourceFormData) => Promise<void>;
}

export const useSourceCRUD = (): UseSourceCrud => {
  const { isReadonly } = useConfig();
  const { persistNamespaces } = useNamespace();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { addPendingItems, removePendingItems } = usePendingStore();
  const { setInstrumentAwait, setInstrumentCount } = useInstrumentStore();
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

  const { fetch: queryAll } = useProxy<{ computePlatform?: { sources?: Source[] } }>(GET_SOURCES);
  const { fetch: queryById } = useProxy<{ computePlatform?: { source?: Source } }, { sourceId: WorkloadId }>(GET_SOURCE);
  const { fetch: mutatePersistSources } = useProxy<{ persistK8sSources: boolean }, SourceInstrumentInput>(PERSIST_SOURCES);
  const { fetch: mutateUpdate } = useProxy<{ updateK8sActualSource: boolean }, { sourceId: WorkloadId; patchSourceRequest: SourceFormData }>(UPDATE_SOURCE);
  const { fetch: queryOtherConditions } = useProxy<{ sourceConditions: SourceConditions[] }>(GET_SOURCE_CONDITIONS);
  const { fetch: querySourceLibraries } = useProxy<{ instrumentationInstanceComponents: InstrumentationInstanceComponent[] }, WorkloadId>(GET_SOURCE_LIBRARIES);

  const shouldFetchSource = (allowFetchDuringLoadTrue?: boolean) => {
    // We should not fetch if we are already fetching.
    const { sourcesLoading } = useEntityStore.getState();
    // We should not fetch while sources are being instrumented.
    const { isAwaitingInstrumentation } = useInstrumentStore.getState();

    return !isAwaitingInstrumentation && (!sourcesLoading || (sourcesLoading && allowFetchDuringLoadTrue));
  };

  const handleInstrumentationCount = (toAddCount: number, toDeleteCount: number) => {
    const { sourcesToCreate, sourcesToDelete } = useInstrumentStore.getState();

    // TODO: estimate the number of instrumentationConfigs to create for future-apps

    if (toDeleteCount > 0) setInstrumentCount('sourcesToDelete', sourcesToDelete + toDeleteCount);
    if (toAddCount > 0) setInstrumentCount('sourcesToCreate', sourcesToCreate + toAddCount);

    if (toDeleteCount > 0 || toAddCount > 0) setInstrumentAwait(true);
  };

  const fetchAllConditions = async () => {
    const sourcesFromStore = useEntityStore.getState().sources;
    const { data } = await queryOtherConditions();
    const { sourceConditions } = data || {};

    if (sourceConditions) {
      const tempSources: Source[] = [];

      for (const item of sourceConditions) {
        const updatedSource = addConditionToSources(item, sourcesFromStore);
        if (updatedSource) tempSources.push(updatedSource);
      }

      addEntities(EntityTypes.Source, tempSources);
    }
  };

  const fetchSources = async () => {
    if (!shouldFetchSource()) return;
    setEntitiesLoading(EntityTypes.Source, true);

    const { data } = await queryAll();
    const { sources: fetchedSources } = data?.computePlatform || {};

    if (fetchedSources) {
      setEntities(EntityTypes.Source, fetchedSources);
      setEntitiesLoading(EntityTypes.Source, false);
      if (fetchedSources.length) fetchAllConditions();
    }
  };

  const fetchSourceById = async (id: WorkloadId, bypassPaginationLoader: boolean = false): Promise<Source | undefined> => {
    if (!shouldFetchSource(bypassPaginationLoader)) return;

    const { data } = await queryById({ sourceId: id });
    const { source } = data?.computePlatform || {};

    if (source) {
      addEntities(EntityTypes.Source, [source]);
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
      addPendingItems([{ entityType: EntityTypes.Source, entityId: sourceId }]);

      await mutateUpdate({ sourceId, patchSourceRequest: payload });

      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${sourceId.name}" source`, sourceId);
      removePendingItems([{ entityType: EntityTypes.Source, entityId: sourceId }]);

      // !! no "fetch"
      // !! we should wait for SSE to handle that
    }
  };

  useEffect(() => {
    if (!sources.length && !sourcesLoading) fetchSources();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    sources,
    sourcesLoading,
    fetchSources,
    fetchSourceById,
    fetchSourceLibraries: (req) => querySourceLibraries(req.variables),
    persistSources,
    updateSource,
  };
};
