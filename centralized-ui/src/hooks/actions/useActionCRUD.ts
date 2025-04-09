import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import type { FetchedAction } from '@/types';
import type { ActionFormData } from '@odigos/ui-kit/types';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { mapActionsFormToGqlInput, mapFetchedActions } from '@/utils';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { CREATE_ACTION, DELETE_ACTION, GET_ACTIONS, UPDATE_ACTION } from '@/graphql';
import { type Action, ActionType, Crud, EntityTypes, StatusType } from '@odigos/ui-kit/types';

interface UseActionCrud {
  actions: Action[];
  actionsLoading: boolean;
  fetchActions: () => void;
  createAction: (action: ActionFormData) => void;
  updateAction: (id: string, action: ActionFormData) => void;
  deleteAction: (id: string, actionType: ActionType) => void;
}

export const useActionCRUD = (): UseActionCrud => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { actionsLoading, setEntitiesLoading, actions, addEntities, removeEntities } = useEntityStore();

  const notifyUser = (type: StatusType, title: string, message: string, id?: string, hideFromHistory?: boolean) => {
    addNotification({
      type,
      title,
      message,
      crdType: EntityTypes.Action,
      target: id ? getSseTargetFromId(id, EntityTypes.Action) : undefined,
      hideFromHistory,
    });
  };

  const { fetch: queryAll } = useProxy<{ computePlatform?: { actions?: FetchedAction[] } }>(GET_ACTIONS);
  const { fetch: mutateCreate } = useProxy<{ createAction: { id: string; type: ActionType } }>(CREATE_ACTION);
  const { fetch: mutateUpdate } = useProxy<{ updateAction: { id: string; type: ActionType } }>(UPDATE_ACTION);
  const { fetch: mutateDelete } = useProxy<{ deleteAction: boolean }>(DELETE_ACTION);

  const fetchActions: UseActionCrud['fetchActions'] = async () => {
    setEntitiesLoading(EntityTypes.Action, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.actions) {
      const { actions: items } = data.computePlatform;

      addEntities(EntityTypes.Action, mapFetchedActions(items));
      setEntitiesLoading(EntityTypes.Action, false);
    }
  };

  const createAction: UseActionCrud['createAction'] = async (action) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateCreate({ action: mapActionsFormToGqlInput({ ...action }) });
      const id = res.data?.createAction.id;
      notifyUser(StatusType.Success, Crud.Create, `Successfully created "${action.type}" action`, id);
      fetchActions();
    }
  };

  const updateAction: UseActionCrud['updateAction'] = async (id, action) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      await mutateUpdate({ id, action: mapActionsFormToGqlInput({ ...action }) });
      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${action.type}" action`, id);
      fetchActions();
    }
  };

  const deleteAction: UseActionCrud['deleteAction'] = async (id, actionType) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      await mutateDelete({ id, actionType });
      notifyUser(StatusType.Success, Crud.Delete, `Successfully deleted "${actionType}" action`, id);
      removeEntities(EntityTypes.Action, [id]);
    }
  };

  useEffect(() => {
    if (!actions.length && !actionsLoading) fetchActions();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    actions,
    actionsLoading,
    fetchActions,
    createAction,
    updateAction,
    deleteAction,
  };
};
