import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import type { ActionInput, FetchedAction } from '@/types';
import type { ActionFormData } from '@odigos/ui-kit/types';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
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

const stringifyRenames = (action: ActionFormData): ActionInput => {
  return {
    ...action,
    fields: {
      ...action.fields,
      renames: action.fields.renames ? JSON.stringify(action.fields.renames) : null,
    },
  };
};

const parseRenames = (action: FetchedAction): Action => {
  return {
    ...action,
    fields: {
      ...action.fields,
      renames: action.fields.renames ? JSON.parse(action.fields.renames) : null,
    },
  };
};

export const useActionCRUD = (): UseActionCrud => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { actionsLoading, setEntitiesLoading, actions, setEntities, removeEntities } = useEntityStore();

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
  const { fetch: mutateCreate } = useProxy<{ createAction: FetchedAction }, { action: ActionInput }>(CREATE_ACTION);
  const { fetch: mutateUpdate } = useProxy<{ updateAction: FetchedAction }, { id: string; action: ActionInput }>(UPDATE_ACTION);
  const { fetch: mutateDelete } = useProxy<{ deleteAction: boolean }>(DELETE_ACTION);

  const fetchActions: UseActionCrud['fetchActions'] = async () => {
    setEntitiesLoading(EntityTypes.Action, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.actions) {
      const { actions: items } = data.computePlatform;

      setEntities(EntityTypes.Action, items.map(parseRenames));
      setEntitiesLoading(EntityTypes.Action, false);
    }
  };

  const createAction: UseActionCrud['createAction'] = async (payload) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateCreate({ action: stringifyRenames(payload) });
      const action = res.data?.createAction;
      if (action) {
        const { id, type } = action;
        // addEntities(EntityTypes.Action, [action]);
        fetchActions(); // refetch because of conditions
        notifyUser(StatusType.Success, Crud.Create, `Successfully created "${type}" action`, id);
      }
    }
  };

  const updateAction: UseActionCrud['updateAction'] = async (id, payload) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateUpdate({ id, action: stringifyRenames(payload) });
      const action = res.data?.updateAction;
      if (action) {
        const { id, type } = action;
        // addEntities(EntityTypes.Action, [action]);
        fetchActions(); // refetch because of conditions
        notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${type}" action`, id);
      }
    }
  };

  const deleteAction: UseActionCrud['deleteAction'] = async (id, actionType) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateDelete({ id, actionType });
      const ok = res.data?.deleteAction;
      if (ok) {
        removeEntities(EntityTypes.Action, [id]);
        notifyUser(StatusType.Success, Crud.Delete, `Successfully deleted "${actionType}" action`, id);
      }
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
