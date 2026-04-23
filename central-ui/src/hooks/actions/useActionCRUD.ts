import { useEffect } from 'react';
import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { stripNullFields } from '@/utils';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import type { ActionInput, ComputePlatform, FetchedAction } from '@/types';
import { useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { type ActionFormData, type CreateActionFunc } from '@odigos/ui-kit/types';
import { CREATE_ACTION, DELETE_ACTION, GET_ACTIONS, UPDATE_ACTION } from '@/graphql';
import { type Action, ActionType, Crud, EntityTypes, StatusType } from '@odigos/ui-kit/types';

interface UseActionCrud {
  actions: Action[];
  actionsLoading: boolean;
  fetchActions: () => void;
  createAction: CreateActionFunc;
  updateAction: (id: string, action: ActionFormData) => void;
  deleteAction: (id: string, actionType: ActionType) => void;
}

// Drops items from a *-attributes array whose `from` field is missing/empty,
// mirroring the validation the backend expects (empty "from" is invalid).
const sanitizeFromArray = <T extends { from?: string | null }>(arr?: T[]) => {
  if (!Array.isArray(arr)) return arr as unknown as T[] | undefined;
  return arr.map((item) => {
    const fromVal = (item as { from?: unknown }).from as string | undefined;
    const hasValidFrom = typeof fromVal === 'string' && fromVal.trim().length > 0;
    if (!hasValidFrom) {
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { from: _from, ...rest } = item as Record<string, unknown>;
      return rest as T;
    }
    return item;
  });
};

// Strips blank entries from URL-templatization groups so the backend doesn't
// reject the whole action for having an empty "kind" / "template" somewhere.
const sanitizeUrlTemplatizationGroups = (
  groups: ActionFormData['fields']['urlTemplatizationRulesGroups'],
): ActionFormData['fields']['urlTemplatizationRulesGroups'] => {
  if (!Array.isArray(groups)) return groups;

  return groups.map((group) => {
    const sanitizedGroup = { ...group };

    const kind = sanitizedGroup.filterK8sWorkloadKind as string | undefined | null;
    if (kind === '') {
      sanitizedGroup.filterK8sWorkloadKind = undefined;
    }

    if (Array.isArray(sanitizedGroup.workloadFilters)) {
      const validFilters = sanitizedGroup.workloadFilters.filter(
        (f) => f.kind && typeof f.kind === 'string' && f.kind.trim() !== '',
      );
      sanitizedGroup.workloadFilters = validFilters.length > 0 ? validFilters : undefined;
    }

    if (Array.isArray(sanitizedGroup.templatizationRules)) {
      sanitizedGroup.templatizationRules = sanitizedGroup.templatizationRules.filter(
        (r) => r.template && typeof r.template === 'string' && r.template.trim() !== '',
      );
    }

    return sanitizedGroup;
  });
};

const stringifyRenames = (action: ActionFormData): ActionInput => {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars, @typescript-eslint/no-explicit-any
  const { id: _id, conditions: _conditions, ...actionInput } = action as ActionFormData & { id?: string; conditions?: unknown };

  return stripNullFields({
    ...actionInput,
    fields: {
      ...action.fields,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      labelsAttributes: sanitizeFromArray(action.fields.labelsAttributes as any),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      annotationsAttributes: sanitizeFromArray(action.fields.annotationsAttributes as any),
      renames: action.fields.renames ? JSON.stringify(action.fields.renames) : null,
      urlTemplatizationRulesGroups: sanitizeUrlTemplatizationGroups(action.fields.urlTemplatizationRulesGroups),
    },
  });
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

export const useActionCRUD = (connections?: ComputePlatform[]): UseActionCrud => {
  const { isVm } = useOdigos();
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

  const { fetch: queryAll } = useRemoteFetch<{ computePlatform?: { actions?: FetchedAction[] } }>(GET_ACTIONS);
  const { fetch: mutateCreate, fetchMulti: mutateCreateMulti } = useRemoteFetch<{ createAction: FetchedAction }, { action: ActionInput }>(CREATE_ACTION, connections);
  const { fetch: mutateUpdate } = useRemoteFetch<{ updateAction: FetchedAction }, { id: string; action: ActionInput }>(UPDATE_ACTION);
  const { fetch: mutateDelete } = useRemoteFetch<{ deleteAction: boolean }>(DELETE_ACTION);

  const fetchActions: UseActionCrud['fetchActions'] = async () => {
    // we do this to prevent unwanted "toast errors" in the UI
    if (isVm) return;

    setEntitiesLoading(EntityTypes.Action, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.actions) {
      const { actions: items } = data.computePlatform;
      setEntities(EntityTypes.Action, items.map(parseRenames));
    }

    setEntitiesLoading(EntityTypes.Action, false);
  };

  const createAction: UseActionCrud['createAction'] = async (payload, connectionIds) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    }
    if (connectionIds?.length) {
      const res = await mutateCreateMulti(connectionIds, { action: stringifyRenames(payload) });
      const error = res.error;
      if (error) {
        notifyUser(StatusType.Error, Crud.Create, error);
        return { error };
      }
      notifyUser(StatusType.Success, Crud.Create, 'Successfully created action');
      return undefined;
    }

    const res = await mutateCreate({ action: stringifyRenames(payload) });
    const action = res.data?.createAction;
    if (action) {
      const { id, type } = action;
      fetchActions();
      notifyUser(StatusType.Success, Crud.Create, `Successfully created "${type}" action`, id);
      return undefined;
    }
    if (res.error) return { error: res.error };
    return undefined;
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
