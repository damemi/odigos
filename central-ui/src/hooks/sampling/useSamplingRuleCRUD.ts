import { useState, useEffect, useCallback } from 'react';
import { useRemoteFetch } from '../proxy';
import { useNotificationStore } from '@odigos/ui-kit/store';
import {
  Crud,
  StatusType,
  type SamplingRules,
  type NoisyOperationRule,
  type NoisyOperationRuleInput,
  type HighlyRelevantOperationRule,
  type HighlyRelevantOperationRuleInput,
  type CostReductionRule,
  type CostReductionRuleInput,
} from '@odigos/ui-kit/types';
import {
  GET_SAMPLING_RULES,
  CREATE_NOISY_OPERATION_RULE,
  UPDATE_NOISY_OPERATION_RULE,
  DELETE_NOISY_OPERATION_RULE,
  CREATE_HIGHLY_RELEVANT_OPERATION_RULE,
  UPDATE_HIGHLY_RELEVANT_OPERATION_RULE,
  DELETE_HIGHLY_RELEVANT_OPERATION_RULE,
  CREATE_COST_REDUCTION_RULE,
  UPDATE_COST_REDUCTION_RULE,
  DELETE_COST_REDUCTION_RULE,
  UPDATE_LOCAL_UI_SAMPLING_CONFIG,
} from '@/graphql';
import type { K8sHealthProbesSamplingConfig, SamplingQueryResponse, SamplingRuleType } from '@/types';

interface UseSamplingRuleCrud {
  samplingRules: SamplingRules[];
  k8sHealthProbesConfig: K8sHealthProbesSamplingConfig | null;
  loading: boolean;
  fetchSamplingRules: () => void;

  createNoisyOperationRule: (samplingId: string, rule: NoisyOperationRuleInput) => void;
  updateNoisyOperationRule: (samplingId: string, ruleId: string, rule: NoisyOperationRuleInput) => void;

  createHighlyRelevantOperationRule: (samplingId: string, rule: HighlyRelevantOperationRuleInput) => void;
  updateHighlyRelevantOperationRule: (samplingId: string, ruleId: string, rule: HighlyRelevantOperationRuleInput) => void;

  createCostReductionRule: (samplingId: string, rule: CostReductionRuleInput) => void;
  updateCostReductionRule: (samplingId: string, ruleId: string, rule: CostReductionRuleInput) => void;

  deleteSamplingRule: (samplingId: string, ruleId: string, category: SamplingRuleType) => void;

  updateK8sHealthProbesConfig: (enabled: boolean, keepPercentage: number) => void;
}

function updateGroup(groups: SamplingRules[], samplingId: string, updater: (group: SamplingRules) => SamplingRules): SamplingRules[] {
  const found = groups.some((g) => g.id === samplingId);
  if (found) {
    return groups.map((g) => (g.id === samplingId ? updater(g) : g));
  }

  const emptyGroup: SamplingRules = { id: samplingId, noisyOperations: [], highlyRelevantOperations: [], costReductionRules: [] };
  return [...groups, updater(emptyGroup)];
}

export const useSamplingRuleCRUD = (): UseSamplingRuleCrud => {
  const { addNotification } = useNotificationStore();
  const [samplingRules, setSamplingRules] = useState<SamplingRules[]>([]);
  const [k8sHealthProbesConfig, setK8sHealthProbesConfig] = useState<K8sHealthProbesSamplingConfig | null>(null);
  const [loading, setLoading] = useState(false);

  const notifyUser = useCallback(
    (type: StatusType, title: string, message: string) => {
      addNotification({ type, title, message });
    },
    [addNotification],
  );

  const { fetch: fetchAll } = useRemoteFetch<SamplingQueryResponse>(GET_SAMPLING_RULES);

  const { fetch: mutateCreateNoisy } = useRemoteFetch<
    { createNoisyOperationRule: NoisyOperationRule },
    { samplingId: string; rule: NoisyOperationRuleInput }
  >(CREATE_NOISY_OPERATION_RULE);
  const { fetch: mutateUpdateNoisy } = useRemoteFetch<
    { updateNoisyOperationRule: NoisyOperationRule },
    { samplingId: string; ruleId: string; rule: NoisyOperationRuleInput }
  >(UPDATE_NOISY_OPERATION_RULE);
  const { fetch: mutateDeleteNoisy } = useRemoteFetch<
    { deleteNoisyOperationRule: boolean },
    { samplingId: string; ruleId: string }
  >(DELETE_NOISY_OPERATION_RULE);

  const { fetch: mutateCreateHighlyRelevant } = useRemoteFetch<
    { createHighlyRelevantOperationRule: HighlyRelevantOperationRule },
    { samplingId: string; rule: HighlyRelevantOperationRuleInput }
  >(CREATE_HIGHLY_RELEVANT_OPERATION_RULE);
  const { fetch: mutateUpdateHighlyRelevant } = useRemoteFetch<
    { updateHighlyRelevantOperationRule: HighlyRelevantOperationRule },
    { samplingId: string; ruleId: string; rule: HighlyRelevantOperationRuleInput }
  >(UPDATE_HIGHLY_RELEVANT_OPERATION_RULE);
  const { fetch: mutateDeleteHighlyRelevant } = useRemoteFetch<
    { deleteHighlyRelevantOperationRule: boolean },
    { samplingId: string; ruleId: string }
  >(DELETE_HIGHLY_RELEVANT_OPERATION_RULE);

  const { fetch: mutateCreateCostReduction } = useRemoteFetch<
    { createCostReductionRule: CostReductionRule },
    { samplingId: string; rule: CostReductionRuleInput }
  >(CREATE_COST_REDUCTION_RULE);
  const { fetch: mutateUpdateCostReduction } = useRemoteFetch<
    { updateCostReductionRule: CostReductionRule },
    { samplingId: string; ruleId: string; rule: CostReductionRuleInput }
  >(UPDATE_COST_REDUCTION_RULE);
  const { fetch: mutateDeleteCostReduction } = useRemoteFetch<
    { deleteCostReductionRule: boolean },
    { samplingId: string; ruleId: string }
  >(DELETE_COST_REDUCTION_RULE);

  const { fetch: mutateUpdateConfig } = useRemoteFetch<
    { updateLocalUiSamplingConfig: boolean },
    { config: { k8sHealthProbesSampling: { enabled: boolean; keepPercentage: number } } }
  >(UPDATE_LOCAL_UI_SAMPLING_CONFIG);

  const fetchSamplingRules = useCallback(async () => {
    setLoading(true);
    const { data } = await fetchAll();
    if (data?.sampling) {
      setSamplingRules(data.sampling.rules);
      setK8sHealthProbesConfig(data.sampling.configs?.effective?.k8sHealthProbesSampling ?? null);
    }
    setLoading(false);
  }, [fetchAll]);

  // ---- Noisy Operations ----

  const createNoisyOperationRule: UseSamplingRuleCrud['createNoisyOperationRule'] = async (samplingId, rule) => {
    const { data } = await mutateCreateNoisy({ samplingId, rule });
    const created = data?.createNoisyOperationRule;
    if (!created) return;
    setSamplingRules((prev) => updateGroup(prev, samplingId, (g) => ({ ...g, noisyOperations: [...g.noisyOperations, created] })));
    notifyUser(StatusType.Success, Crud.Create, `Successfully created noisy operation rule "${created.name || created.ruleId}"`);
  };

  const updateNoisyOperationRule: UseSamplingRuleCrud['updateNoisyOperationRule'] = async (samplingId, ruleId, rule) => {
    const { data } = await mutateUpdateNoisy({ samplingId, ruleId, rule });
    const updated = data?.updateNoisyOperationRule;
    if (!updated) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({
        ...g,
        noisyOperations: g.noisyOperations.map((r) => (r.ruleId === ruleId ? updated : r)),
      })),
    );
    notifyUser(StatusType.Success, Crud.Update, `Successfully updated noisy operation rule "${updated.name || updated.ruleId}"`);
  };

  const deleteNoisyOperationRule = async (samplingId: string, ruleId: string) => {
    const { data } = await mutateDeleteNoisy({ samplingId, ruleId });
    if (!data?.deleteNoisyOperationRule) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({
        ...g,
        noisyOperations: g.noisyOperations.filter((r) => r.ruleId !== ruleId),
      })),
    );
    notifyUser(StatusType.Success, Crud.Delete, 'Successfully deleted noisy operation rule');
  };

  // ---- Highly Relevant Operations ----

  const createHighlyRelevantOperationRule: UseSamplingRuleCrud['createHighlyRelevantOperationRule'] = async (samplingId, rule) => {
    const { data } = await mutateCreateHighlyRelevant({ samplingId, rule });
    const created = data?.createHighlyRelevantOperationRule;
    if (!created) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({ ...g, highlyRelevantOperations: [...g.highlyRelevantOperations, created] })),
    );
    notifyUser(StatusType.Success, Crud.Create, `Successfully created highly relevant operation rule "${created.name || created.ruleId}"`);
  };

  const updateHighlyRelevantOperationRule: UseSamplingRuleCrud['updateHighlyRelevantOperationRule'] = async (samplingId, ruleId, rule) => {
    const { data } = await mutateUpdateHighlyRelevant({ samplingId, ruleId, rule });
    const updated = data?.updateHighlyRelevantOperationRule;
    if (!updated) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({
        ...g,
        highlyRelevantOperations: g.highlyRelevantOperations.map((r) => (r.ruleId === ruleId ? updated : r)),
      })),
    );
    notifyUser(StatusType.Success, Crud.Update, `Successfully updated highly relevant operation rule "${updated.name || updated.ruleId}"`);
  };

  const deleteHighlyRelevantOperationRule = async (samplingId: string, ruleId: string) => {
    const { data } = await mutateDeleteHighlyRelevant({ samplingId, ruleId });
    if (!data?.deleteHighlyRelevantOperationRule) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({
        ...g,
        highlyRelevantOperations: g.highlyRelevantOperations.filter((r) => r.ruleId !== ruleId),
      })),
    );
    notifyUser(StatusType.Success, Crud.Delete, 'Successfully deleted highly relevant operation rule');
  };

  // ---- Cost Reduction Rules ----

  const createCostReductionRule: UseSamplingRuleCrud['createCostReductionRule'] = async (samplingId, rule) => {
    const { data } = await mutateCreateCostReduction({ samplingId, rule });
    const created = data?.createCostReductionRule;
    if (!created) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({ ...g, costReductionRules: [...g.costReductionRules, created] })),
    );
    notifyUser(StatusType.Success, Crud.Create, `Successfully created cost reduction rule "${created.name || created.ruleId}"`);
  };

  const updateCostReductionRule: UseSamplingRuleCrud['updateCostReductionRule'] = async (samplingId, ruleId, rule) => {
    const { data } = await mutateUpdateCostReduction({ samplingId, ruleId, rule });
    const updated = data?.updateCostReductionRule;
    if (!updated) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({
        ...g,
        costReductionRules: g.costReductionRules.map((r) => (r.ruleId === ruleId ? updated : r)),
      })),
    );
    notifyUser(StatusType.Success, Crud.Update, `Successfully updated cost reduction rule "${updated.name || updated.ruleId}"`);
  };

  const deleteCostReductionRule = async (samplingId: string, ruleId: string) => {
    const { data } = await mutateDeleteCostReduction({ samplingId, ruleId });
    if (!data?.deleteCostReductionRule) return;
    setSamplingRules((prev) =>
      updateGroup(prev, samplingId, (g) => ({
        ...g,
        costReductionRules: g.costReductionRules.filter((r) => r.ruleId !== ruleId),
      })),
    );
    notifyUser(StatusType.Success, Crud.Delete, 'Successfully deleted cost reduction rule');
  };

  // ---- Delete dispatcher ----

  const deleteSamplingRule: UseSamplingRuleCrud['deleteSamplingRule'] = (samplingId, ruleId, category) => {
    switch (category) {
      case 'noisy':
        deleteNoisyOperationRule(samplingId, ruleId);
        break;
      case 'highlyRelevant':
        deleteHighlyRelevantOperationRule(samplingId, ruleId);
        break;
      case 'costReduction':
        deleteCostReductionRule(samplingId, ruleId);
        break;
    }
  };

  // ---- Config ----

  const updateK8sHealthProbesConfig: UseSamplingRuleCrud['updateK8sHealthProbesConfig'] = async (enabled, keepPercentage) => {
    const { data } = await mutateUpdateConfig({ config: { k8sHealthProbesSampling: { enabled, keepPercentage } } });
    if (!data?.updateLocalUiSamplingConfig) return;
    setK8sHealthProbesConfig({ enabled, keepPercentage });
    notifyUser(StatusType.Success, Crud.Update, 'Successfully updated auto rule configuration');
  };

  useEffect(() => {
    fetchSamplingRules();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return {
    samplingRules,
    k8sHealthProbesConfig,
    loading,
    fetchSamplingRules,
    createNoisyOperationRule,
    updateNoisyOperationRule,
    createHighlyRelevantOperationRule,
    updateHighlyRelevantOperationRule,
    createCostReductionRule,
    updateCostReductionRule,
    deleteSamplingRule,
    updateK8sHealthProbesConfig,
  };
};
