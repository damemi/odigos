import { useEffect } from 'react';
import { useConfig } from '../config';
import { useProxyStore } from '@/store';
import { useRemoteFetch } from '../proxy';
import { mapFetchedDestinations, pickFallbackProxyID } from '@/utils';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useDataStreamStore, useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { CREATE_DESTINATION, DELETE_DESTINATION, GET_DESTINATIONS, UPDATE_DESTINATION } from '@/graphql';
import { EntityTypes, type Destination, type DestinationFormData, StatusType, Crud, type CreateDestinationFunc, type UpdateDestinationFunc } from '@odigos/ui-kit/types';
import type { ComputePlatform } from '@/types';

interface UseDestinationCrud {
  destinations: Destination[];
  destinationsLoading: boolean;
  fetchDestinations: () => Promise<void>;
  createDestination: CreateDestinationFunc;
  updateDestination: UpdateDestinationFunc;
  deleteDestination: (id: string) => Promise<void>;
}

const mapNoUndefinedFields = (destination: DestinationFormData, selectedStreamName: string) => ({
  ...destination,
  fields: destination.fields.filter(({ value }) => value !== undefined),
  currentStreamName: selectedStreamName,
});

/**
 * @param connections Optional list of all known compute platforms. Required
 * when the hook is used from a page that drives bulk operations across
 * multiple clusters (e.g. the central connections page) — both for resolving
 * a fallback proxy on single-cluster calls and for fanning create/update
 * mutations out via `fetchMulti`.
 */
export const useDestinationCRUD = (connections: ComputePlatform[] = []): UseDestinationCrud => {
  const { isReadonly } = useConfig();
  const { proxyID } = useProxyStore();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { destinationsLoading, setEntitiesLoading, destinations, setEntities } = useEntityStore();

  const notifyUser = (type: StatusType, title: string, message: string, id?: string, hideFromHistory?: boolean) => {
    addNotification({
      type,
      title,
      message,
      crdType: EntityTypes.Destination,
      target: id ? getSseTargetFromId(id, EntityTypes.Destination) : undefined,
      hideFromHistory,
    });
  };

  const { fetch: queryAll } = useRemoteFetch<{ computePlatform: { destinations: Destination[] } }>(GET_DESTINATIONS);
  const { fetch: mutateCreate, fetchMulti: mutateCreateMulti } = useRemoteFetch<{ createNewDestination: Destination }, { destination: DestinationFormData }>(CREATE_DESTINATION, connections);
  const { fetch: mutateUpdate, fetchMulti: mutateUpdateMulti } = useRemoteFetch<{ updateDestination: { id: string } }, { id: string; destination: DestinationFormData }>(
    UPDATE_DESTINATION,
    connections,
  );
  const { fetch: mutateDelete } = useRemoteFetch<{ deleteDestination: boolean }, { id: string; currentStreamName: string }>(DELETE_DESTINATION);

  const fetchDestinations = async () => {
    setEntitiesLoading(EntityTypes.Destination, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.destinations) {
      const { destinations: items } = data.computePlatform;
      setEntities(EntityTypes.Destination, mapFetchedDestinations(items));
    }

    setEntitiesLoading(EntityTypes.Destination, false);
  };

  const createDestination: CreateDestinationFunc = async (destination, connectionIds) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    }

    const payload = { destination: mapNoUndefinedFields(destination, selectedStreamName) };

    if (connectionIds?.length) {
      const res = await mutateCreateMulti(connectionIds, payload);
      if (res.error) {
        notifyUser(StatusType.Error, Crud.Create, res.error);
        return { error: res.error };
      }
      notifyUser(StatusType.Success, Crud.Create, `Successfully created "${destination.type}" destination`);
      return undefined;
    }

    const targetProxyID = proxyID || pickFallbackProxyID(connections);
    const { error } = await mutateCreate(payload, targetProxyID);
    if (error) return { error };
    // Refetch immediately instead of waiting for SSE (backend throttle + client-side debounce
    // can delay the overview update by several seconds and make it appear empty).
    await fetchDestinations();
    return undefined;
  };

  const updateDestination: UpdateDestinationFunc = async (id, destination, connectionIds) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    }

    const payload = { id, destination: mapNoUndefinedFields(destination, selectedStreamName) };

    if (connectionIds?.length) {
      const res = await mutateUpdateMulti(connectionIds, payload);
      if (res.error) {
        notifyUser(StatusType.Error, Crud.Update, res.error);
        return { error: res.error };
      }
      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${destination.type}" destination`, id);
      return undefined;
    }

    const targetProxyID = proxyID || pickFallbackProxyID(connections);
    const { data, error } = await mutateUpdate(payload, targetProxyID);

    if (error) return { error };

    if (data?.updateDestination) {
      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${destination.type}" destination`, id);
      await fetchDestinations();
    }
    return undefined;
  };

  const deleteDestination: UseDestinationCrud['deleteDestination'] = async (id) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      await mutateDelete({ id, currentStreamName: selectedStreamName });
      await fetchDestinations();
    }
  };

  useEffect(() => {
    if (!destinations.length && !destinationsLoading) fetchDestinations();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    destinations,
    destinationsLoading,
    fetchDestinations,
    createDestination,
    updateDestination,
    deleteDestination,
  };
};
