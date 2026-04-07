import { useEffect } from 'react';
import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
import { mapFetchedDestinations } from '@/utils';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useDataStreamStore, useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { CREATE_DESTINATION, DELETE_DESTINATION, GET_DESTINATIONS, UPDATE_DESTINATION } from '@/graphql';
import { EntityTypes, type Destination, type DestinationFormData, StatusType, Crud } from '@odigos/ui-kit/types';

interface UseDestinationCrud {
  destinations: Destination[];
  destinationsLoading: boolean;
  fetchDestinations: () => Promise<void>;
  createDestination: (destination: DestinationFormData) => Promise<{ error?: string } | undefined>;
  updateDestination: (id: string, destination: DestinationFormData) => Promise<{ error?: string } | undefined>;
  deleteDestination: (id: string) => Promise<void>;
}

const mapNoUndefinedFields = (destination: DestinationFormData, selectedStreamName: string) => ({
  ...destination,
  fields: destination.fields.filter(({ value }) => value !== undefined),
  currentStreamName: selectedStreamName,
});

export const useDestinationCRUD = (): UseDestinationCrud => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { selectedStreamName } = useDataStreamStore();
  const { destinationsLoading, setEntitiesLoading, destinations, setEntities, addEntities } = useEntityStore();

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
  const { fetch: mutateCreate } = useRemoteFetch<{ createNewDestination: Destination }, { destination: DestinationFormData }>(CREATE_DESTINATION);
  const { fetch: mutateUpdate } = useRemoteFetch<{ updateDestination: { id: string } }, { id: string; destination: DestinationFormData }>(UPDATE_DESTINATION);
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

  const createDestination: UseDestinationCrud['createDestination'] = async (destination) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    }
    const { error } = await mutateCreate({ destination: mapNoUndefinedFields(destination, selectedStreamName) });
    if (error) return { error };
    return undefined;
  };

  const updateDestination: UseDestinationCrud['updateDestination'] = async (id, destination) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    }
    const { data, error } = await mutateUpdate({ id, destination: mapNoUndefinedFields(destination, selectedStreamName) });

    if (error) return { error };

    if (data?.updateDestination) {
      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${destination.type}" destination`, id);
    }
    return undefined;
  };

  const deleteDestination: UseDestinationCrud['deleteDestination'] = async (id) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      await mutateDelete({ id, currentStreamName: selectedStreamName });
      // !! no "fetch", and no "notifyUser"
      // !! we should wait for SSE to handle that
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
