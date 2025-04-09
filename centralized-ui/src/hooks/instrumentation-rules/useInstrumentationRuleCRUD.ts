import { useEffect } from 'react';
import { useProxy } from '../proxy';
import { useConfig } from '../config';
import { getSseTargetFromId } from '@odigos/ui-kit/functions';
import { DISPLAY_TITLES, FORM_ALERTS } from '@odigos/ui-kit/constants';
import { useEntityStore, useNotificationStore } from '@odigos/ui-kit/store';
import { Crud, EntityTypes, StatusType, type InstrumentationRule, type InstrumentationRuleFormData } from '@odigos/ui-kit/types';
import { CREATE_INSTRUMENTATION_RULE, DELETE_INSTURMENTATION_RULE, GET_INSTRUMENTATION_RULES, UPDATE_INSTURMENTATION_RULE } from '@/graphql';

interface UseInstrumentationRuleCrud {
  instrumentationRules: InstrumentationRule[];
  instrumentationRulesLoading: boolean;
  fetchInstrumentationRules: () => void;
  createInstrumentationRule: (instrumentationRule: InstrumentationRuleFormData) => void;
  updateInstrumentationRule: (ruleId: string, instrumentationRule: InstrumentationRuleFormData) => void;
  deleteInstrumentationRule: (ruleId: string) => void;
}

export const useInstrumentationRuleCRUD = (): UseInstrumentationRuleCrud => {
  const { isReadonly } = useConfig();
  const { addNotification } = useNotificationStore();
  const { instrumentationRulesLoading, setEntitiesLoading, instrumentationRules, addEntities, removeEntities } = useEntityStore();

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

  const { fetch: queryAll } = useProxy<{ computePlatform?: { instrumentationRules?: InstrumentationRule[] } }>(GET_INSTRUMENTATION_RULES);
  const { fetch: mutateCreate } = useProxy<{ createInstrumentationRule: InstrumentationRule }>(CREATE_INSTRUMENTATION_RULE);
  const { fetch: mutateUpdate } = useProxy<{ updateInstrumentationRule: InstrumentationRule }>(UPDATE_INSTURMENTATION_RULE);
  const { fetch: mutateDelete } = useProxy<{ deleteInstrumentationRule: boolean }>(DELETE_INSTURMENTATION_RULE);

  const fetchInstrumentationRules = async () => {
    setEntitiesLoading(EntityTypes.InstrumentationRule, true);
    const { data } = await queryAll();

    if (data?.computePlatform?.instrumentationRules) {
      const { instrumentationRules: items } = data.computePlatform;

      addEntities(EntityTypes.InstrumentationRule, items);
      setEntitiesLoading(EntityTypes.InstrumentationRule, false);
    }
  };

  const createInstrumentationRule: UseInstrumentationRuleCrud['createInstrumentationRule'] = async (instrumentationRule) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateCreate({ instrumentationRule });
      const rule = res.data?.createInstrumentationRule as InstrumentationRule;
      addEntities(EntityTypes.InstrumentationRule, [rule]);
      notifyUser(StatusType.Success, Crud.Create, `Successfully created "${rule.type}" rule`, rule.ruleId);
    }
  };

  const updateInstrumentationRule: UseInstrumentationRuleCrud['updateInstrumentationRule'] = async (ruleId, instrumentationRule) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      const res = await mutateUpdate({ ruleId, instrumentationRule });
      const rule = res.data?.updateInstrumentationRule as InstrumentationRule;
      addEntities(EntityTypes.InstrumentationRule, [rule]);
      notifyUser(StatusType.Success, Crud.Update, `Successfully updated "${rule.type}" rule`, rule.ruleId);
    }
  };

  const deleteInstrumentationRule: UseInstrumentationRuleCrud['deleteInstrumentationRule'] = async (ruleId) => {
    if (isReadonly) {
      notifyUser(StatusType.Warning, DISPLAY_TITLES.READONLY, FORM_ALERTS.READONLY_WARNING, undefined, true);
    } else {
      await mutateDelete({ ruleId });
      const rule = instrumentationRules.find((r) => r.ruleId === ruleId);
      const type = rule ? rule.type : '';
      removeEntities(EntityTypes.InstrumentationRule, [ruleId]);
      notifyUser(StatusType.Success, Crud.Delete, `Successfully deleted "${type || ruleId}" rule`, ruleId);
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
