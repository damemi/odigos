import { useEffect } from 'react';
import { useConfig } from '../config';
import { useRemoteFetch } from '../proxy';
import type { ComputePlatform } from '@/types';
import { useOdigos } from '@odigos/ui-kit/contexts';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { CREATE_INSTRUMENTATION_RULE, DELETE_INSTURMENTATION_RULE, GET_INSTRUMENTATION_RULES, UPDATE_INSTURMENTATION_RULE } from '@/graphql';
import { Crud, EntityTypes, StatusType, type InstrumentationRule, type InstrumentationRuleFormData, type CreateInstrumentationRuleFunc } from '@odigos/ui-kit/types';
import { adaptInstrumentationRuleFromWire, adaptInstrumentationRuleInputForWire, getSseTargetFromId, type InstrumentationRuleInputWire, type InstrumentationRuleWire } from '@odigos/ui-kit/functions';

interface UseInstrumentationRuleCrud {
  instrumentationRules: InstrumentationRule[];
  instrumentationRulesLoading: boolean;
  fetchInstrumentationRules: () => void;
  createInstrumentationRule: CreateInstrumentationRuleFunc;
  updateInstrumentationRule: (ruleId: string, instrumentationRule: InstrumentationRuleFormData) => void;
  deleteInstrumentationRule: (ruleId: string) => void;
}

export const useInstrumentationRuleCRUD = (connections?: ComputePlatform[]): UseInstrumentationRuleCrud => {
  const { isVm } = useOdigos();
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { instrumentationRulesLoading, setEntitiesLoading, instrumentationRules, setEntities, addEntities, removeEntities } = useEntityStore();

  const notifyUser = (type: StatusType, title: string, message: string, id?: string, hideFromHistory?: boolean) => {
    addNotification({
      type,
      title,
      message,
      crdType: EntityTypes.InstrumentationRule,
      target: id ? getSseTargetFromId(id, EntityTypes.InstrumentationRule) : undefined,
      hideFromHistory,
    });
  };

  // Wire shapes are the row-per-criterion `sourcesScopes` form returned by the
  // GraphQL backend; we adapt to the kit's spec-shape `sourceScopes` at the
  // boundary so everything downstream (entity store, drawers, forms) sees a
  // single canonical shape.
  const { fetch: queryAll } = useRemoteFetch<{ computePlatform?: { instrumentationRules?: InstrumentationRuleWire[] } }>(GET_INSTRUMENTATION_RULES);
  const { fetch: mutateCreate, fetchMulti: mutateCreateMulti } = useRemoteFetch<{ createInstrumentationRule: InstrumentationRuleWire }, { instrumentationRule: InstrumentationRuleInputWire }>(
    CREATE_INSTRUMENTATION_RULE,
    connections,
  );
  const { fetch: mutateUpdate } = useRemoteFetch<{ updateInstrumentationRule: InstrumentationRuleWire }, { ruleId: string; instrumentationRule: InstrumentationRuleInputWire }>(
    UPDATE_INSTURMENTATION_RULE,
  );
  const { fetch: mutateDelete } = useRemoteFetch<{ deleteInstrumentationRule: boolean }>(DELETE_INSTURMENTATION_RULE);

  const fetchInstrumentationRules = async () => {
    // we do this to prevent unwanted "toast errors" in the UI
    if (isVm) return;

    setEntitiesLoading(EntityTypes.InstrumentationRule, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.instrumentationRules) {
      const items = data.computePlatform.instrumentationRules.map(adaptInstrumentationRuleFromWire);
      setEntities(EntityTypes.InstrumentationRule, items);
    }

    setEntitiesLoading(EntityTypes.InstrumentationRule, false);
  };

  const createInstrumentationRule: UseInstrumentationRuleCrud['createInstrumentationRule'] = async (instrumentationRule, connectionIds) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
      return { error: FORM_ALERTS.READONLY_WARNING };
    } else {
      const wireInput = adaptInstrumentationRuleInputForWire(instrumentationRule);
      if (connectionIds?.length) {
        const res = await mutateCreateMulti(connectionIds, { instrumentationRule: wireInput });
        const error = res.error;
        if (error) {
          notifyUser(StatusType.Error, Crud.Create, error);
          return { error };
        }
        notifyUser(StatusType.Success, Crud.Create, 'Successfully created rule');
        return undefined;
      }

      const res = await mutateCreate({ instrumentationRule: wireInput });
      const wireRule = res.data?.createInstrumentationRule;
      if (wireRule) {
        const rule = adaptInstrumentationRuleFromWire(wireRule);
        const { ruleId, type } = rule;
        addEntities(EntityTypes.InstrumentationRule, [rule]);
        notifyUser(StatusType.Success, Crud.Create, `Successfully created "${type}" rule`, ruleId);
        return undefined;
      }
      if (res.error) return { error: res.error };
    }
    return undefined;
  };

  const updateInstrumentationRule: UseInstrumentationRuleCrud['updateInstrumentationRule'] = async (ruleId, instrumentationRule) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateUpdate({ ruleId, instrumentationRule: adaptInstrumentationRuleInputForWire(instrumentationRule) });
      const wireRule = res.data?.updateInstrumentationRule;
      if (wireRule) {
        const rule = adaptInstrumentationRuleFromWire(wireRule);
        const { ruleId, type } = rule;
        addEntities(EntityTypes.InstrumentationRule, [rule]);
        notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${type}" rule`, ruleId);
      }
    }
  };

  const deleteInstrumentationRule: UseInstrumentationRuleCrud['deleteInstrumentationRule'] = async (ruleId) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateDelete({ ruleId });
      const ok = res.data?.deleteInstrumentationRule;
      if (ok) {
        const { type } = instrumentationRules.find((r) => r.ruleId === ruleId) || {};
        removeEntities(EntityTypes.InstrumentationRule, [ruleId]);
        notifyUser(StatusType.Success, Crud.Delete, `Successfully deleted "${type || ruleId}" rule`, ruleId);
      }
    }
  };

  useEffect(() => {
    if (!instrumentationRules.length && !instrumentationRulesLoading) fetchInstrumentationRules();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    instrumentationRules,
    instrumentationRulesLoading,
    fetchInstrumentationRules,
    createInstrumentationRule,
    updateInstrumentationRule,
    deleteInstrumentationRule,
  };
};
