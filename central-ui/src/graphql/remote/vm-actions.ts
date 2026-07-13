import { generateId } from '@odigos/ui-kit/functions';
import { ActionType as KitActionType, SignalType, type Action, type ActionFormData, type UrlTemplatizationRulesGroup } from '@odigos/ui-kit/types';
import {
  ActionType as VmActionType,
  TelemetrySignal,
  type CreateActionInput,
  type CreateActionMutation,
  type DeleteActionMutation,
  type GetActionsQuery,
  type UpdateActionInput,
  type UpdateActionMutation,
  type UrlTemplatizationConfigInput,
  type UrlTemplatizationRulesGroupInput,
} from '@odigos/vmagent-api-apollo-client';
import type { CreateActionResult, CreateActionVars, DeleteActionResult, DeleteActionVars, GetActionsData, UpdateActionResult, UpdateActionVars } from '@odigos/ui-kit/contexts';
import { stripEmpty } from './utils';

// The agents-api action surface (v0.12.0+) is a discriminated union
// (`URLTemplatizationAction` | `ProcessorAdditionAction` | `SpanMetricsAction`)
// keyed by `actionType`. central-ui's domain model (the ui-kit `Action`) is a
// k8s-oriented model with a different, larger set of action types
// (`K8sAttributes`, `AddClusterInfo`, `DeleteAttribute`, `RenameAttribute`,
// `PiiMasking`, `URLTemplatization`, `ExtractAttribute`).
//
// The ONLY action type representable on both surfaces is URL templatization
// (VM `urlTemplatization` <-> kit `URLTemplatization`), so that's the one this
// adapter maps bidirectionally. VM-only action types (`processorAddition`,
// `spanMetrics`) have no ui-kit `ActionType` and are filtered out of reads
// (the kit can neither render nor edit them); the k8s-only kit types have no VM
// equivalent and can't be created against a VM proxy.

type VmAction = GetActionsQuery['actions'][number];
// The concrete `URLTemplatizationAction` union member (the only one the kit can
// represent). `actionType` is a plain `ActionType` enum shared by every member,
// so it's used for the runtime check, while this alias narrows the member so
// its type-specific `config` is accessible in a type-safe way.
type VmUrlTemplatizationAction = Extract<VmAction, { __typename: 'URLTemplatizationAction' }>;

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

// agents-api action (union member) -> ui-kit `Action`. The VM `name` is the
// resource id (kit `id`); the VM `actionName` is the display label (kit `name`).
// Returns `null` for VM action types the kit can't represent so callers can
// drop them from the list.
export const mapVmActionToAction = (action: VmAction): Action | null => {
  if (action.actionType !== VmActionType.UrlTemplatization) return null;
  const { config } = action as VmUrlTemplatizationAction;

  return {
    id: action.name,
    type: KitActionType.URLTemplatization,
    name: action.actionName ?? null,
    notes: action.notes ?? null,
    disabled: action.disabled,
    signals: toKitSignals(action.signals),
    fields: {
      // VM `examples` (per rule) has no ui-kit field and is dropped on read.
      urlTemplatizationRulesGroups: (config?.templatizationRulesGroups ?? []).map((group) => ({
        notes: group.notes ?? undefined,
        templatizationRules: group.templatizationRules.map((rule) => ({ template: rule.template, notes: rule.notes ?? undefined })),
      })),
    },
    conditions: null,
  };
};

// `GetActions` envelope (`{ actions: [...] }`) -> `GET_ACTIONS` result. VM-only
// action types are filtered out (see `mapVmActionToAction`).
export const mapVmActions = (data: GetActionsQuery | null | undefined): GetActionsData => {
  const actions = data?.actions ?? [];
  return { actions: actions.map(mapVmActionToAction).filter((a): a is Action => a !== null) };
};

// ----- mutation variable mappers (-> agents-api `{ input }`) -----

// The asserts/throws below run inside `transformVariables`, which the adapter's
// root `withGuardedRun` wrapper executes on a guarded `run` path: a throw is
// caught and returned as `{ error }`, which the kit surfaces as a normal error
// toast (it does NOT crash the UI). Rejecting is intentional over silently
// coercing/dropping data the VM can't represent. In practice the VM action
// catalog (`actionTypes`) only advertises URL Templatization, so the drawer
// never offers the other types — these are a defense-in-depth backstop.

// Only URL templatization exists on both the kit and the agents-api action
// surface. Creating/updating any other kit action type (PiiMasking,
// K8sAttributes, ...) against a VM proxy is rejected up front with a clear
// message instead of being silently coerced into a malformed urlTemplatization
// mutation that the agent rejects with a cryptic server-side validation error.
const assertVmSupportedActionType = (type: KitActionType): void => {
  if (type !== KitActionType.URLTemplatization) {
    throw new Error(`Action type "${type}" is not supported by the VM agent. Only "URL Templatization" actions can be created on VM hosts.`);
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
      !!stripEmpty(g.filterK8sNamespace) ||
      !!stripEmpty(g.filterProgrammingLanguage) ||
      !!stripEmpty(g.filterK8sWorkloadKind) ||
      !!stripEmpty(g.filterK8sWorkloadName) ||
      (g.workloadFilters?.length ?? 0) > 0,
  );
  if (hasScoping) {
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
          return notes ? { template: rule.template, notes } : { template: rule.template };
        });
      if (templatizationRules.length === 0) return undefined;
      const notes = stripEmpty(group.notes);
      return notes ? { notes, templatizationRules } : { templatizationRules };
    })
    .filter((group): group is UrlTemplatizationRulesGroupInput => group !== undefined);

  return templatizationRulesGroups.length ? { templatizationRulesGroups } : undefined;
};

export const vmCreateActionVariables = (vars: CreateActionVars): Record<string, unknown> => {
  const action = vars.action;
  assertVmSupportedActionType(action.type);
  assertVmSupportedActionFields(action);

  const actionName = stripEmpty(action.name);
  const notes = stripEmpty(action.notes);
  const urlTemplatization = buildVmUrlTemplatization(action.fields?.urlTemplatizationRulesGroups);
  if (!urlTemplatization) throw new Error('A "URL Templatization" action requires at least one rule group with a template.');

  const input: CreateActionInput = {
    // VM requires a unique resource `name`; the kit only carries the display
    // `name` (-> VM `actionName`), so generate the resource id on create.
    name: generateId(),
    actionType: VmActionType.UrlTemplatization,
    disabled: !!action.disabled,
    signals: toVmSignals(action.signals),
    urlTemplatization,
    ...(actionName ? { actionName } : {}),
    ...(notes ? { notes } : {}),
  };
  return { input };
};

export const vmUpdateActionVariables = (vars: UpdateActionVars): Record<string, unknown> => {
  const action = vars.action;
  assertVmSupportedActionType(action.type);
  assertVmSupportedActionFields(action);

  const actionName = stripEmpty(action.name);
  const notes = stripEmpty(action.notes);
  const urlTemplatization = buildVmUrlTemplatization(action.fields?.urlTemplatizationRulesGroups);
  if (!urlTemplatization) throw new Error('A "URL Templatization" action requires at least one rule group with a template.');

  const input: UpdateActionInput = {
    name: vars.id,
    actionType: VmActionType.UrlTemplatization,
    disabled: !!action.disabled,
    signals: toVmSignals(action.signals),
    urlTemplatization,
    ...(actionName ? { actionName } : {}),
    ...(notes ? { notes } : {}),
  };
  return { input };
};

export const vmDeleteActionVariables = (vars: DeleteActionVars): Record<string, unknown> => ({ input: { name: vars.id } });

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
