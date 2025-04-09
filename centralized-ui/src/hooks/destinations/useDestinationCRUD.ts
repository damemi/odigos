import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import { mapFetchedDestinations } from '@/utils';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useEntityStore, useNotificationStore, usePendingStore } from '@odigos/ui-kit/store';
import { CREATE_DESTINATION, DELETE_DESTINATION, GET_DESTINATIONS, UPDATE_DESTINATION } from '@/graphql';
import { EntityTypes, type Destination, type DestinationFormData, StatusType, Crud } from '@odigos/ui-kit/types';

interface UseDestinationCrud {
  destinations: Destination[];
  destinationsLoading: boolean;
  fetchDestinations: () => void;
  createDestination: (destination: DestinationFormData) => void;
  updateDestination: (id: string, destination: DestinationFormData) => void;
  deleteDestination: (id: string) => void;
}

export const useDestinationCRUD = (): UseDestinationCrud => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { addPendingItems, removePendingItems } = usePendingStore();
  const { destinationsLoading, setEntitiesLoading, destinations, addEntities, removeEntities } = useEntityStore();

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

  const { fetch: queryAll } = useProxy<{ computePlatform: { destinations: Destination[] } }>(GET_DESTINATIONS);
  const { fetch: mutateCreate } = useProxy<{ createNewDestination: Destination }>(CREATE_DESTINATION);
  const { fetch: mutateUpdate } = useProxy<{ updateDestination: { id: string } }>(UPDATE_DESTINATION);
  const { fetch: mutateDelete } = useProxy<{ deleteDestination: boolean }>(DELETE_DESTINATION);

  const fetchDestinations = async () => {
    setEntitiesLoading(EntityTypes.Destination, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.destinations) {
      const { destinations: items } = data.computePlatform;
      addEntities(EntityTypes.Destination, mapFetchedDestinations(items));
      setEntitiesLoading(EntityTypes.Destination, false);
    }
  };

  const createDestination: UseDestinationCrud['createDestination'] = async (destination) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateCreate({ destination: { ...destination, fields: destination.fields.filter(({ value }) => value !== undefined) } });
      const created = res.data?.createNewDestination as Destination;
      addEntities(EntityTypes.Destination, mapFetchedDestinations([created]));
      notifyUser(StatusType.Success, Crud.Create, `Successfully created "${created.destinationType.type}" destination`, created.id);
    }
  };

  const updateDestination: UseDestinationCrud['updateDestination'] = async (id, destination) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      addPendingItems([{ entityType: EntityTypes.Destination, entityId: id }]);
      notifyUser(StatusType.Default, 'Pending', 'Updating destination...', undefined, true);
      await mutateUpdate({ id, destination: { ...destination, fields: destination.fields.filter(({ value }) => value !== undefined) } });
      removePendingItems([{ entityType: EntityTypes.Destination, entityId: id }]);
      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${destination.type}" destination`, id);
      // no updating store, we wait for SSE
      // TODO: SSE for centralized
    }
  };

  const deleteDestination: UseDestinationCrud['deleteDestination'] = async (id) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      await mutateDelete({ id });
      const destination = destinations.find((r) => r.id === id);
      removeEntities(EntityTypes.Destination, [id]);
      notifyUser(StatusType.Success, Crud.Delete, `Successfully deleted "${destination?.destinationType.type || id}" destination`, id);
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
