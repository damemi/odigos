import { generateId } from '@odigos/ui-kit/functions';
import { ActionKeyTypes, ActionType as KitActionType, PlatformType, SignalType, type Action, type ActionFormData, type Extraction, type UrlTemplatizationRulesGroup } from '@odigos/ui-kit/types';
import {
  ActionType as VmActionType,
  TelemetrySignal,
  type CreateActionInput,
  type CreateActionMutation,
  type DeleteActionMutation,
  type GetActionsQuery,
  type UpdateActionInput,
  type UpdateActionMutation,
  type ProcessorAdditionConfigInput,
  type SpanMetricsConfigInput,
  type UrlTemplatizationConfigInput,
  type UrlTemplatizationRulesGroupInput,
} from '@odigos/vmagent-api-apollo-client';
import type { CreateActionResult, CreateActionVars, DeleteActionResult, DeleteActionVars, GetActionsData, UpdateActionResult, UpdateActionVars } from '@odigos/ui-kit/contexts';
import { stripEmpty, supportsActionRegion, withFieldSelections } from './utils';
import { dialectVersion } from './vm-dialect';
import type { OperationContext } from '@odigos/ui-kit/contexts';

// The agents-api action surface (v0.12.0+) is a discriminated union
// (`URLTemplatizationAction` | `ProcessorAdditionAction` | `SpanMetricsAction`)
// keyed by `actionType`. central-ui's domain model (the ui-kit `Action`) is a
// k8s-oriented model with a different, larger set of action types
// (`K8sAttributes`, `AddClusterInfo`, `DeleteAttribute`, `RenameAttribute`,
// `PiiMasking`, `URLTemplatization`, `ExtractAttribute`).
//
// The UI kit uses a platform-neutral field bag; this adapter owns conversion
// to the typed agents-api configuration branches. PII masking remains excluded.

type VmAction = GetActionsQuery['actions'][number];
type VmUrlTemplatizationAction = Extract<VmAction, { __typename: 'URLTemplatizationAction' }>;
type VmProcessorAdditionAction = Extract<VmAction, { __typename: 'ProcessorAdditionAction' }>;
type VmSpanMetricsAction = Extract<VmAction, { __typename: 'SpanMetricsAction' }>;

// kit <-> agents-api telemetry signal. Both enums use identical string values
// (LOGS / METRICS / TRACES / PROFILES), but map explicitly so an enum drift in
// either package surfaces as a compile error rather than a silent miscast.
const SIGNAL_TO_VM: Record<SignalType, TelemetrySignal> = {
  [SignalType.Logs]: TelemetrySignal.Logs,
  [SignalType.Metrics]: TelemetrySignal.Metrics,
  [SignalType.Traces]: TelemetrySignal.Traces,
  [SignalType.Profiles]: TelemetrySignal.Profiles,
};
const SIGNAL_FROM_VM: Record<TelemetrySignal, SignalType> = {
  [TelemetrySignal.Logs]: SignalType.Logs,
  [TelemetrySignal.Metrics]: SignalType.Metrics,
  [TelemetrySignal.Traces]: SignalType.Traces,
  [TelemetrySignal.Profiles]: SignalType.Profiles,
};

const toKitSignals = (signals: TelemetrySignal[] | null | undefined): SignalType[] => (signals ?? []).map((s) => SIGNAL_FROM_VM[s]).filter((s): s is SignalType => !!s);
const toVmSignals = (signals: SignalType[] | null | undefined): TelemetrySignal[] => (signals ?? []).map((s) => SIGNAL_TO_VM[s]).filter((s): s is TelemetrySignal => !!s);

// The published dialect snapshots predate Action.region. The adapter injects
// it for the connector schema, so retain the small local intersection until a
// generated snapshot selects it natively.
type VmActionWithRegion = VmAction & { region?: string | null };

const baseAction = (action: VmAction) => ({
  id: action.name,
  region: (action as VmActionWithRegion).region ?? null,
  name: action.actionName ?? null,
  notes: action.notes ?? null,
  disabled: action.disabled,
  signals: toKitSignals(action.signals),
  conditions: null,
});

export const patchVmActionsQuery = (printedQuery: string, ctx: OperationContext): string =>
  ctx.platformType === PlatformType.Connector && supportsActionRegion(dialectVersion(ctx)) ? withFieldSelections(printedQuery, 'actions', 'region') : printedQuery;

// agents-api action (union member) -> ui-kit `Action`. The VM `name` is the
// resource id (kit `id`); the VM `actionName` is the display label (kit `name`).
export const mapVmActionToAction = (action: VmAction): Action | null => {
  switch (action.actionType) {
    case VmActionType.UrlTemplatization: {
      const { config } = action as VmUrlTemplatizationAction;
      return {
        ...baseAction(action),
        type: KitActionType.URLTemplatization,
        fields: {
          // VM `examples` (per rule) has no ui-kit field and is dropped on read.
          [ActionKeyTypes.UrlTemplatizationRulesGroups]: (config?.templatizationRulesGroups ?? []).map((group) => ({
            notes: group.notes ?? undefined,
            templatizationRules: group.templatizationRules.map((rule) => ({ template: rule.template, notes: rule.notes ?? undefined, examples: rule.examples ?? undefined })),
          })),
        },
      };
    }
    case VmActionType.ProcessorAddition: {
      const { config } = action as VmProcessorAdditionAction;
      return {
        ...baseAction(action),
        type: KitActionType.ProcessorAddition,
        fields: {
          [ActionKeyTypes.ProcessorType]: config?.type ?? '',
          [ActionKeyTypes.ProcessorName]: config?.processorName ?? '',
          [ActionKeyTypes.ProcessorConfig]: JSON.stringify(config?.processorConfig ?? {}, null, 2),
        },
      };
    }
    case VmActionType.SpanMetrics: {
      const { config } = action as VmSpanMetricsAction;
      return {
        ...baseAction(action),
        type: KitActionType.SpanMetrics,
        fields: {
          [ActionKeyTypes.SpanMetricsDisabled]: config?.disabled ?? false,
          [ActionKeyTypes.Interval]: config?.interval ?? '',
          [ActionKeyTypes.MetricsExpiration]: config?.metricsExpiration ?? '',
          [ActionKeyTypes.AdditionalDimensions]: config?.additionalDimensions ?? [],
          [ActionKeyTypes.HistogramDisabled]: config?.histogramDisabled ?? false,
          [ActionKeyTypes.ExplicitHistogramBuckets]: config?.explicitHistogramBuckets ?? [],
          [ActionKeyTypes.ResourceMetricsKeyAttributes]: config?.resourceMetricsKeyAttributes ?? [],
          [ActionKeyTypes.IncludedProcessInDimensions]: config?.includedProcessInDimensions ?? false,
          [ActionKeyTypes.ExcludedResourceAttributes]: config?.excludedResourceAttributes ?? [],
        },
      };
    }
    case VmActionType.DbQueryTemplatization: {
      const { config } = action as Extract<VmAction, { __typename: 'DbQueryTemplatizationAction' }>;
      return { ...baseAction(action), type: KitActionType.DbQueryTemplatization, fields: {
        [ActionKeyTypes.TemplatizeLiterals]: config?.templatizeLiterals ?? false,
        [ActionKeyTypes.RemovePostgresCastOperator]: config?.removePostgresCastOperator ?? false,
      } };
    }
    case VmActionType.InferDbAttributes:
      return { ...baseAction(action), type: KitActionType.InferDbAttributes, fields: {} };
    case VmActionType.ExtractAttribute: {
      const { config } = action as Extract<VmAction, { __typename: 'ExtractAttributeAction' }>;
      return { ...baseAction(action), type: KitActionType.ExtractAttribute, fields: {
        [ActionKeyTypes.ExtractAttribute]: { extractions: (config?.extractions ?? []).map((row) => ({
          targetAttributeName: row.targetAttributeName,
          ...(row.lookupKey ? { lookupKey: row.lookupKey } : {}),
          ...(row.dataFormat ? { dataFormat: row.dataFormat as Extraction['dataFormat'] } : {}),
          ...(row.regex ? { regex: row.regex } : {}),
        })) },
      } };
    }
    case VmActionType.DeleteAttribute: {
      const { config } = action as Extract<VmAction, { __typename: 'DeleteAttributeAction' }>;
      return { ...baseAction(action), type: KitActionType.DeleteAttributes, fields: { [ActionKeyTypes.AttributeNamesToDelete]: config?.attributeNamesToDelete ?? [] } };
    }
    case VmActionType.RenameAttribute: {
      const { config } = action as Extract<VmAction, { __typename: 'RenameAttributeAction' }>;
      return { ...baseAction(action), type: KitActionType.RenameAttributes, fields: { [ActionKeyTypes.Renames]: Object.fromEntries((config?.renames ?? []).map(({ from, to }) => [from, to])) } };
    }
    default:
      return null;
  }
};

// `GetActions` envelope (`{ actions: [...] }`) -> `GET_ACTIONS` result.
export const mapVmActions = (data: GetActionsQuery | null | undefined): GetActionsData => {
  const actions = data?.actions ?? [];
  return { actions: actions.map(mapVmActionToAction).filter((a): a is Action => a !== null) };
};

// ----- mutation variable mappers (-> agents-api `{ input }`) -----

const assertVmSupportedActionType = (type: KitActionType): void => {
  if (![KitActionType.URLTemplatization, KitActionType.ProcessorAddition, KitActionType.SpanMetrics, KitActionType.DbQueryTemplatization, KitActionType.InferDbAttributes, KitActionType.ExtractAttribute, KitActionType.DeleteAttributes, KitActionType.RenameAttributes].includes(type)) {
    throw new Error(`Action type "${type}" is not supported by this collector.`);
  }
};

// The VM `UrlTemplatizationRulesGroupInput` only carries `notes` + the list of
// rules — it has no group-level scoping (the k8s namespace/workload/language
// filters the kit form exposes). Those filters would be silently dropped here,
// so reject the mutation with a clear error rather than applying a templatization
// action far more broadly than the user intended.
const assertVmSupportedActionFields = (action: ActionFormData): void => {
  const groups = action.fields?.urlTemplatizationRulesGroups ?? [];
  const hasScoping = groups.some(
    (g) =>
      !!g.scopes ||
      !!stripEmpty(g.filterK8sNamespace) ||
      !!stripEmpty(g.filterProgrammingLanguage) ||
      !!stripEmpty(g.filterK8sWorkloadKind) ||
      !!stripEmpty(g.filterK8sWorkloadName) ||
      (g.workloadFilters?.length ?? 0) > 0,
  );
  if (hasScoping || action.fields?.scopes || action.fields?.urlTemplatizationDefaultGroups?.length) {
    throw new Error('The VM agent does not support URL Templatization scoping (namespace / workload / programming-language filters). These would be silently ignored — remove them, or manage this action on a Kubernetes platform.');
  }
};

// Build the VM url-templatization config from the kit form data, dropping empty
// optional fields (group/rule notes) and any rule without a template or group
// without rules. Returns `undefined` when nothing meaningful remains so the
// caller can omit the field entirely — the agent's `required` validation
// rejects an empty `templatizationRulesGroups` array.
const buildVmUrlTemplatization = (groups: UrlTemplatizationRulesGroup[] | null | undefined): UrlTemplatizationConfigInput | undefined => {
  const templatizationRulesGroups = (groups ?? [])
    .map((group): UrlTemplatizationRulesGroupInput | undefined => {
      const templatizationRules = (group.templatizationRules ?? [])
        .filter((rule) => !!stripEmpty(rule?.template))
        .map((rule) => {
          const notes = stripEmpty(rule.notes);
          return { template: rule.template, ...(notes ? { notes } : {}), ...(rule.examples ? { examples: rule.examples } : {}) };
        });
      if (templatizationRules.length === 0) return undefined;
      const notes = stripEmpty(group.notes);
      return notes ? { notes, templatizationRules } : { templatizationRules };
    })
    .filter((group): group is UrlTemplatizationRulesGroupInput => group !== undefined);

  return templatizationRulesGroups.length ? { templatizationRulesGroups } : undefined;
};

const asStringArray = (value: unknown): string[] | undefined => {
  if (!Array.isArray(value)) return undefined;
  return value.map((item) => String(item).trim()).filter(Boolean);
};

const buildProcessorAddition = (action: ActionFormData): ProcessorAdditionConfigInput => {
  const type = String(action.fields?.[ActionKeyTypes.ProcessorType] ?? '').trim();
  if (!type) throw new Error('Processor type is required.');
  const rawConfig = action.fields?.[ActionKeyTypes.ProcessorConfig];
  let processorConfig: unknown;
  try {
    processorConfig = typeof rawConfig === 'string' ? JSON.parse(rawConfig || '{}') : rawConfig;
  } catch {
    throw new Error('Processor configuration must be valid JSON.');
  }
  if (!processorConfig || typeof processorConfig !== 'object' || Array.isArray(processorConfig)) {
    throw new Error('Processor configuration must be a JSON object.');
  }
  const processorName = stripEmpty(action.fields?.[ActionKeyTypes.ProcessorName] as string | null | undefined);
  return { type, processorConfig: processorConfig as Record<string, unknown>, ...(processorName ? { processorName } : {}) };
};

const buildAdditionalAction = (action: ActionFormData): Pick<CreateActionInput, 'actionType' | 'dbQueryTemplatization' | 'extractAttribute' | 'deleteAttribute' | 'renameAttribute'> => {
  const fields = action.fields ?? {};
  switch (action.type) {
    case KitActionType.DbQueryTemplatization: {
      const templatizeLiterals = fields[ActionKeyTypes.TemplatizeLiterals] === true;
      const removePostgresCastOperator = fields[ActionKeyTypes.RemovePostgresCastOperator] === true;
      if (!templatizeLiterals && !removePostgresCastOperator) throw new Error('Enable literal templatization or PostgreSQL cast removal.');
      return { actionType: VmActionType.DbQueryTemplatization, dbQueryTemplatization: { templatizeLiterals, removePostgresCastOperator } };
    }
    case KitActionType.InferDbAttributes:
      return { actionType: VmActionType.InferDbAttributes };
    case KitActionType.DeleteAttributes: {
      const attributeNamesToDelete = asStringArray(fields[ActionKeyTypes.AttributeNamesToDelete]) ?? [];
      if (!attributeNamesToDelete.length) throw new Error('At least one attribute name is required.');
      return { actionType: VmActionType.DeleteAttribute, deleteAttribute: { attributeNamesToDelete } };
    }
    case KitActionType.RenameAttributes: {
      const renames = Object.entries(fields[ActionKeyTypes.Renames] ?? {}).map(([from, to]) => ({ from: from.trim(), to: to.trim() }));
      if (!renames.length || renames.some(({ from, to }) => !from || !to || from === to)) throw new Error('Each rename needs different, non-blank old and new names.');
      return { actionType: VmActionType.RenameAttribute, renameAttribute: { renames } };
    }
    case KitActionType.ExtractAttribute: {
      const extractions = (fields[ActionKeyTypes.ExtractAttribute]?.extractions ?? []).map((row) => ({
        targetAttributeName: row.targetAttributeName.trim(),
        ...(stripEmpty(row.lookupKey) ? { lookupKey: row.lookupKey!.trim() } : {}),
        ...(row.dataFormat ? { dataFormat: row.dataFormat } : {}),
        ...(stripEmpty(row.regex) ? { regex: row.regex!.trim() } : {}),
      }));
      if (!extractions.length || extractions.some((row) => !row.targetAttributeName || (!!row.regex === !!row.lookupKey) || (!!row.lookupKey !== !!row.dataFormat))) throw new Error('Each extraction needs a target and either a regex or a lookup key with data format.');
      return { actionType: VmActionType.ExtractAttribute, extractAttribute: { extractions } };
    }
    default:
      throw new Error(`Action type "${action.type}" is not supported by this collector.`);
  }
};

const buildSpanMetrics = (action: ActionFormData): SpanMetricsConfigInput => {
  const fields = action.fields ?? {};
  const text = (key: ActionKeyTypes): string | undefined => stripEmpty(fields[key] as string | null | undefined);
  const bool = (key: ActionKeyTypes): boolean | undefined => (typeof fields[key] === 'boolean' ? fields[key] : undefined);
  return {
    ...(bool(ActionKeyTypes.SpanMetricsDisabled) !== undefined ? { disabled: bool(ActionKeyTypes.SpanMetricsDisabled) } : {}),
    ...(text(ActionKeyTypes.Interval) ? { interval: text(ActionKeyTypes.Interval) } : {}),
    ...(text(ActionKeyTypes.MetricsExpiration) ? { metricsExpiration: text(ActionKeyTypes.MetricsExpiration) } : {}),
    ...(asStringArray(fields[ActionKeyTypes.AdditionalDimensions]) ? { additionalDimensions: asStringArray(fields[ActionKeyTypes.AdditionalDimensions]) } : {}),
    ...(bool(ActionKeyTypes.HistogramDisabled) !== undefined ? { histogramDisabled: bool(ActionKeyTypes.HistogramDisabled) } : {}),
    ...(asStringArray(fields[ActionKeyTypes.ExplicitHistogramBuckets]) ? { explicitHistogramBuckets: asStringArray(fields[ActionKeyTypes.ExplicitHistogramBuckets]) } : {}),
    ...(asStringArray(fields[ActionKeyTypes.ResourceMetricsKeyAttributes]) ? { resourceMetricsKeyAttributes: asStringArray(fields[ActionKeyTypes.ResourceMetricsKeyAttributes]) } : {}),
    ...(bool(ActionKeyTypes.IncludedProcessInDimensions) !== undefined ? { includedProcessInDimensions: bool(ActionKeyTypes.IncludedProcessInDimensions) } : {}),
    ...(asStringArray(fields[ActionKeyTypes.ExcludedResourceAttributes]) ? { excludedResourceAttributes: asStringArray(fields[ActionKeyTypes.ExcludedResourceAttributes]) } : {}),
  };
};

export const vmCreateActionVariables = (vars: CreateActionVars): Record<string, unknown> => {
  const action = vars.action;
  assertVmSupportedActionType(action.type);
  assertVmSupportedActionFields(action);

  const actionName = stripEmpty(action.name);
  const notes = stripEmpty(action.notes);
	const region = stripEmpty(action.region);
  const base = {
    // VM requires a unique resource `name`; the kit only carries the display
    // `name` (-> VM `actionName`), so generate the resource id on create.
    name: generateId(),
    disabled: !!action.disabled,
    signals: toVmSignals(action.signals),
    ...(actionName ? { actionName } : {}),
    ...(notes ? { notes } : {}),
	...(region ? { region } : {}),
  };
  switch (action.type) {
    case KitActionType.URLTemplatization: {
      assertVmSupportedActionFields(action);
      const urlTemplatization = buildVmUrlTemplatization(action.fields?.urlTemplatizationRulesGroups);
      if (!urlTemplatization) throw new Error('A "URL Templatization" action requires at least one rule group with a template.');
      return { input: { ...base, actionType: VmActionType.UrlTemplatization, urlTemplatization } satisfies CreateActionInput };
    }
    case KitActionType.ProcessorAddition:
      return { input: { ...base, actionType: VmActionType.ProcessorAddition, processorAddition: buildProcessorAddition(action) } satisfies CreateActionInput };
    case KitActionType.SpanMetrics:
      return { input: { ...base, actionType: VmActionType.SpanMetrics, spanMetrics: buildSpanMetrics(action) } satisfies CreateActionInput };
    default:
      return { input: { ...base, ...buildAdditionalAction(action) } satisfies CreateActionInput };
  }
  throw new Error(`Action type "${action.type}" is not supported by this collector.`);
};

export const vmUpdateActionVariables = (vars: UpdateActionVars): Record<string, unknown> => {
  const action = vars.action;
  assertVmSupportedActionType(action.type);
  assertVmSupportedActionFields(action);

  const actionName = stripEmpty(action.name);
  const notes = stripEmpty(action.notes);
	const region = stripEmpty(action.region);
  const base = {
    name: vars.id,
    disabled: !!action.disabled,
    signals: toVmSignals(action.signals),
    ...(actionName ? { actionName } : {}),
    ...(notes ? { notes } : {}),
	...(region ? { region } : {}),
  };
  switch (action.type) {
    case KitActionType.URLTemplatization: {
      assertVmSupportedActionFields(action);
      const urlTemplatization = buildVmUrlTemplatization(action.fields?.urlTemplatizationRulesGroups);
      if (!urlTemplatization) throw new Error('A "URL Templatization" action requires at least one rule group with a template.');
      return { input: { ...base, actionType: VmActionType.UrlTemplatization, urlTemplatization } satisfies UpdateActionInput };
    }
    case KitActionType.ProcessorAddition:
      return { input: { ...base, actionType: VmActionType.ProcessorAddition, processorAddition: buildProcessorAddition(action) } satisfies UpdateActionInput };
    case KitActionType.SpanMetrics:
      return { input: { ...base, actionType: VmActionType.SpanMetrics, spanMetrics: buildSpanMetrics(action) } satisfies UpdateActionInput };
    default:
      return { input: { ...base, ...buildAdditionalAction(action) } satisfies UpdateActionInput };
  }
  throw new Error(`Action type "${action.type}" is not supported by this collector.`);
};

export const vmDeleteActionVariables = (vars: DeleteActionVars): Record<string, unknown> => ({ input: { name: vars.id, ...(stripEmpty(vars.region) ? { region: stripEmpty(vars.region) } : {}) } });

// ----- mutation result mappers -----

export const vmCreateActionResult = (data: CreateActionMutation | null | undefined): CreateActionResult | undefined => {
  const created = data?.createAction;
  const mapped = created ? mapVmActionToAction(created) : null;
  return mapped ? { createAction: mapped } : undefined;
};

export const vmUpdateActionResult = (data: UpdateActionMutation | null | undefined): UpdateActionResult | undefined => {
  const updated = data?.updateAction;
  const mapped = updated ? mapVmActionToAction(updated) : null;
  return mapped ? { updateAction: mapped } : undefined;
};

export const vmDeleteActionResult = (data: DeleteActionMutation | null | undefined): DeleteActionResult | undefined => ({ deleteAction: !!data?.deleteAction?.deleted });
