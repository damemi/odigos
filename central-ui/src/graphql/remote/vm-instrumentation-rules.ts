import { generateId } from '@odigos/ui-kit/functions';
import { GolangCustomProbe, InstrumentationRuleType, JavaCustomProbe, type InstrumentationRule } from '@odigos/ui-kit/types';
import {
  RuleType,
  type CreateInstrumentationRuleInput,
  type CreateInstrumentationRuleMutation,
  type DeleteInstrumentationRuleMutation,
  type GetInstrumentationRulesQuery,
  type UpdateInstrumentationRuleInput,
  type UpdateInstrumentationRuleMutation,
} from '@odigos/vmagent-api-apollo-client';
import type {
  CreateInstrumentationRuleResult,
  CreateInstrumentationRuleVars,
  DeleteInstrumentationRuleResult,
  DeleteInstrumentationRuleVars,
  GetInstrumentationRulesData,
  UpdateInstrumentationRuleResult,
  UpdateInstrumentationRuleVars,
} from '@odigos/ui-kit/contexts';
import { stripEmpty } from './utils';
import { assertAgentCanWriteActionsAndRules } from './vm-dialect';
import type { OperationContext } from '@odigos/ui-kit/contexts';

// The agents-api instrumentation-rule surface (v0.12.0+) is a discriminated
// union (`CodeAttributesRule` | `CustomInstrumentationRule` |
// `HeadersCollectionRule` | `PayloadCollectionRule` | `KernelTracesRule`) keyed
// by `ruleType` (plus `SamplingRule`, which the Sampling page owns), with
// a per-type `config`. central-ui's domain model (the ui-kit `InstrumentationRule`)
// is a single flat shape carrying one optional config object per rule type. The
// VM surface is also narrower than k8s: rules are global (no `sourceScopes` /
// `workloads` — kit scopes are stripped on create/update), and
// `payloadCollection` is a set of booleans rather than per-signal config
// objects. These helpers translate between the two surfaces (resolved from the
// version-pinned dialect documents), filling the VM-absent fields with neutral
// defaults.

type VmRule = GetInstrumentationRulesQuery['instrumentationRules'][number];
// `ruleType` is a plain `RuleType` enum shared by every union member, so it's
// used for the runtime switch below, while this alias narrows a member so its
// type-specific `config` is accessible in a type-safe way.
type VmRuleConfigOf<T extends VmRule['__typename']> = Extract<VmRule, { __typename: T }>['config'];

// agents-api `RuleType` -> ui-kit `InstrumentationRuleType`. The string values
// line up across both enums; unknown future types degrade to `UnknownType`.
const toKitRuleType = (ruleType: RuleType): InstrumentationRuleType => {
  switch (ruleType) {
    case RuleType.CodeAttributes:
      return InstrumentationRuleType.CodeAttributes;
    case RuleType.HeadersCollection:
      return InstrumentationRuleType.HeadersCollection;
    case RuleType.PayloadCollection:
      return InstrumentationRuleType.PayloadCollection;
    case RuleType.CustomInstrumentation:
      return InstrumentationRuleType.CustomInstrumentation;
    case RuleType.KernelTraces:
      return InstrumentationRuleType.KernelTraces;
    default:
      return InstrumentationRuleType.UnknownType;
  }
};

// agents-api rule (union member) -> ui-kit `InstrumentationRule`. VM has no
// `name`-vs-`id` split (the name is the id), no scopes, and no enable/notes
// metadata, so those are pinned to neutral defaults. The kit's rules hook folds
// the (absent) `sourcesScopes` wire field, so leaving scopes null is correct.
export const mapVmRuleToInstrumentationRule = (rule: VmRule): InstrumentationRule => {
  const base: InstrumentationRule = {
    ruleId: rule.name,
    ruleName: rule.name,
    type: toKitRuleType(rule.ruleType),
    notes: rule.notes ?? '',
    disabled: !rule.enabled,
    mutable: true,
    profileName: '',
    sourceScopes: null,
    workloads: null,
    instrumentationLibraries: null,
    conditions: null,
    codeAttributes: null,
    headersCollection: null,
    payloadCollection: null,
    customInstrumentations: null,
    kernelTraces: null,
  };

  switch (rule.ruleType) {
    case RuleType.CodeAttributes: {
      const c = rule.config as VmRuleConfigOf<'CodeAttributesRule'>;
      base.codeAttributes = c
        ? {
            column: c.collectColumn ?? null,
            filePath: c.collectFilePath ?? null,
            function: c.collectFunction ?? null,
            lineNumber: c.collectLineNumber ?? null,
            namespace: c.collectNamespace ?? null,
            stacktrace: c.collectStacktrace ?? null,
          }
        : null;
      return base;
    }
    case RuleType.HeadersCollection: {
      const c = rule.config as VmRuleConfigOf<'HeadersCollectionRule'>;
      base.headersCollection = c ? { headerKeys: c.headerKeys ?? null } : null;
      return base;
    }
    case RuleType.PayloadCollection: {
      const c = rule.config as VmRuleConfigOf<'PayloadCollectionRule'>;
      // VM exposes a single boolean per payload kind; the kit models each as an
      // (optional) config object, so presence === enabled. The richer kit
      // sub-fields (mimeTypes / maxPayloadLength / dropPartialPayloads) have no
      // VM equivalent and stay unset.
      base.payloadCollection = c
        ? {
            httpRequest: c.collectHttpRequest ? {} : null,
            httpResponse: c.collectHttpResponse ? {} : null,
            dbQuery: c.collectDbQuery ? {} : null,
            messaging: c.collectMessaging ? {} : null,
          }
        : null;
      return base;
    }
    case RuleType.CustomInstrumentation: {
      const c = rule.config as VmRuleConfigOf<'CustomInstrumentationRule'>;
      // VM also carries `cpp` probes, which the kit's CustomInstrumentations
      // model doesn't represent (java/golang only) — dropped on read.
      base.customInstrumentations = c
        ? {
            java: c.java?.map((p) => new JavaCustomProbe(p.className, p.methodName)) ?? null,
            golang: c.golang?.map((p) => new GolangCustomProbe(p.packageName, p.functionName ?? null, p.receiverName ?? null, p.receiverMethodName ?? null)) ?? null,
          }
        : null;
      return base;
    }
    case RuleType.KernelTraces: {
      const c = rule.config as VmRuleConfigOf<'KernelTracesRule'>;
      // The agent's catalog renders each operation as a checkbox stored as an
      // object (`checkedValue: emptyObject`), so the kit form treats a present
      // object as checked. Only operations the agent reports as enabled are set.
      base.kernelTraces = c
        ? {
            filesystem: c.filesystem && c.filesystem.enabled !== false ? { enabled: true } : null,
            subprocesses: c.subprocesses ? { recordCommandArgs: c.subprocesses.recordCommandArgs ?? false } : null,
            cpuDelay: c.cpuDelay && c.cpuDelay.enabled !== false ? { enabled: true } : null,
          }
        : null;
      return base;
    }
    default:
      return base;
  }
};

// Agents reporting a schema older than agents-api v0.12.0 serve rules under
// `computePlatform` with an untyped `config`; the `type` strings match the kit's
// `InstrumentationRuleType` values and the config keys its per-type fields.
interface LegacyVmRule {
  name: string;
  type: string;
  config?: Record<string, unknown> | null;
}
export interface LegacyGetInstrumentationRulesQuery {
  computePlatform?: { instrumentationRules?: LegacyVmRule[] | null } | null;
}

const KIT_RULE_TYPES = [InstrumentationRuleType.CodeAttributes, InstrumentationRuleType.HeadersCollection, InstrumentationRuleType.PayloadCollection, InstrumentationRuleType.CustomInstrumentation, InstrumentationRuleType.KernelTraces] as const;
type LegacyKitRuleType = (typeof KIT_RULE_TYPES)[number];
const isLegacyKitRuleType = (type: string): type is LegacyKitRuleType => KIT_RULE_TYPES.some((known) => known === type);

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const flag = (value: unknown): boolean | null => (typeof value === 'boolean' ? value : null);
const nestedFlag = (value: unknown, key: string): { [k: string]: boolean | null } | null => (isRecord(value) ? { [key]: flag(value[key]) } : null);

// Per-type config fields the Rules list can show from untyped legacy config. Custom
// instrumentation probes are not decoded: the rule lists by name and type only.
const legacyRuleConfig = (type: LegacyKitRuleType, config: Record<string, unknown>): Partial<InstrumentationRule> => {
  switch (type) {
    case InstrumentationRuleType.CodeAttributes:
      return { codeAttributes: { column: flag(config.column), filePath: flag(config.filePath), function: flag(config.function), lineNumber: flag(config.lineNumber), namespace: flag(config.namespace), stacktrace: flag(config.stacktrace) } };
    case InstrumentationRuleType.HeadersCollection:
      return { headersCollection: { headerKeys: Array.isArray(config.headerKeys) ? config.headerKeys.filter((k): k is string => typeof k === 'string') : null } };
    case InstrumentationRuleType.PayloadCollection:
      return {
        payloadCollection: {
          httpRequest: isRecord(config.httpRequest) ? {} : null,
          httpResponse: isRecord(config.httpResponse) ? {} : null,
          dbQuery: isRecord(config.dbQuery) ? {} : null,
          messaging: isRecord(config.messaging) ? {} : null,
        },
      };
    case InstrumentationRuleType.KernelTraces:
      return { kernelTraces: { filesystem: nestedFlag(config.filesystem, 'enabled'), subprocesses: nestedFlag(config.subprocesses, 'recordCommandArgs'), cpuDelay: nestedFlag(config.cpuDelay, 'enabled') } };
    default:
      return {};
  }
};

// Legacy agents cannot take the typed write documents, so their rules are listed
// as immutable (the kit disables editing rather than failing on submit).
const mapLegacyVmRule = ({ name, type, config }: LegacyVmRule): InstrumentationRule | null => {
  if (!isLegacyKitRuleType(type)) return null;
  return {
    ruleId: name,
    ruleName: name,
    type,
    notes: '',
    disabled: false,
    mutable: false,
    profileName: '',
    sourceScopes: null,
    workloads: null,
    instrumentationLibraries: null,
    conditions: null,
    codeAttributes: null,
    headersCollection: null,
    payloadCollection: null,
    customInstrumentations: null,
    kernelTraces: null,
    ...legacyRuleConfig(type, config ?? {}),
  };
};

// `GetInstrumentationRules` envelope (`{ instrumentationRules: [...] }`) ->
// `GET_INSTRUMENTATION_RULES` result.
export const mapVmInstrumentationRules = (data: GetInstrumentationRulesQuery | LegacyGetInstrumentationRulesQuery | null | undefined): GetInstrumentationRulesData => {
  const legacy = (data as LegacyGetInstrumentationRulesQuery | null | undefined)?.computePlatform?.instrumentationRules;
  if (legacy) return { instrumentationRules: legacy.map(mapLegacyVmRule).filter((r): r is InstrumentationRule => r !== null) };
  const rules = (data as GetInstrumentationRulesQuery | null | undefined)?.instrumentationRules ?? [];
  // Sampling rules live on the Sampling page, not in the instrumentation rules list.
  return { instrumentationRules: rules.filter((rule) => rule.ruleType !== RuleType.Sampling).map(mapVmRuleToInstrumentationRule) };
};

// ----- mutation variable mappers (-> agents-api `{ input }`) -----

// The kit's wire input (`InstrumentationRuleInputWire`) carries the spec-shape
// config objects but NOT the rule `type` (it's omitted from the form data), so
// the agents-api `ruleType` + matching config branch are inferred from whichever
// config object the form populated. The VM `CreateInstrumentationRuleInput` /
// `UpdateInstrumentationRuleInput` share the same config-branch fields, so a
// single builder serves both.
type VmRuleConfigInput = Pick<CreateInstrumentationRuleInput, 'ruleType' | 'codeAttributes' | 'HeadersCollection' | 'payloadCollection' | 'customInstrumentation' | 'kernelTraces'>;

type VmRuleInput = CreateInstrumentationRuleVars['instrumentationRule'];

const payloadKindHasAdvancedOptions = (kind: { mimeTypes?: string[] | null; maxPayloadLength?: number | null; dropPartialPayloads?: boolean | null } | null | undefined): boolean =>
  !!kind && ((kind.mimeTypes?.length ?? 0) > 0 || kind.maxPayloadLength != null || kind.dropPartialPayloads === true);

// The VM agents-api rule schema is narrower than the kit's k8s rule model.
// `CreateInstrumentationRuleInput`/`UpdateInstrumentationRuleInput` carry
// `name`, `ruleType`, `enabled`, `notes`, and the per-type config branch — but
// NOT the k8s-only targeting fields (source scopes, workloads, conditions,
// instrumentation libraries). Those are stripped below: VM rules are always
// global, and kit callers (e.g. profiling's "Profiling Symbols" rule) still
// attach source scopes for k8s. Dropping them is the correct VM mapping —
// `buildVmRuleConfig` / the mutation input never forward them.
//
// Fields that would lose user-authored config with no VM equivalent (rename,
// payload advanced options) are still rejected up front. The root
// `withGuardedRuns` wrapper turns this throw into a normal error toast.
const assertVmSupportedRuleFields = (rule: VmRuleInput, opts?: { ruleId?: string }): void => {
  const unsupported: string[] = [];

  if (opts?.ruleId && stripEmpty(rule.ruleName) && rule.ruleName !== opts.ruleId) unsupported.push('renaming a rule');
  const pc = rule.payloadCollection;
  if (pc && [pc.httpRequest, pc.httpResponse, pc.dbQuery, pc.messaging].some(payloadKindHasAdvancedOptions)) {
    unsupported.push('payload collection advanced options (mime types / max length / drop partial payloads)');
  }

  if (unsupported.length > 0) {
    throw new Error(
      `The VM agent does not support the following on instrumentation rules: ${unsupported.join(', ')}. These would be silently ignored — remove them, or manage this rule on a Kubernetes platform.`,
    );
  }
};

const buildVmRuleConfig = (rule: CreateInstrumentationRuleVars['instrumentationRule']): VmRuleConfigInput => {
  if (rule.kernelTraces) {
    const { filesystem, subprocesses, cpuDelay } = rule.kernelTraces;
    return {
      ruleType: RuleType.KernelTraces,
      // A checked operation arrives as a present object (`{}`), an unchecked one as absent.
      kernelTraces: {
        ...(filesystem ? { filesystem: { enabled: true } } : {}),
        ...(subprocesses ? { subprocesses: { recordCommandArgs: !!subprocesses.recordCommandArgs } } : {}),
        ...(cpuDelay ? { cpuDelay: { enabled: true } } : {}),
      },
    };
  }
  if (rule.customInstrumentations) {
    const ci = rule.customInstrumentations;
    // VM requires `className`/`methodName` (java) and `packageName` (golang);
    // drop probes missing their required identity instead of sending blanks.
    const java = (ci.java ?? [])
      .filter((p: { className?: string | null; methodName?: string | null }) => !!stripEmpty(p.className) && !!stripEmpty(p.methodName))
      .map((p: { className: string; methodName: string }) => ({ className: p.className, methodName: p.methodName }));
    const golang = (ci.golang ?? [])
      .filter((p: { packageName?: string | null }) => !!stripEmpty(p.packageName))
      .map((p: { packageName: string; functionName?: string | null; receiverName?: string | null; receiverMethodName?: string | null }) => ({
        packageName: p.packageName,
        functionName: stripEmpty(p.functionName),
        receiverName: stripEmpty(p.receiverName),
        receiverMethodName: stripEmpty(p.receiverMethodName),
      }));
    if (java.length === 0 && golang.length === 0) throw new Error('A "Custom Instrumentation" rule requires at least one valid Java (class + method) or Go (package) probe.');
    return {
      ruleType: RuleType.CustomInstrumentation,
      customInstrumentation: {
        ...(java.length ? { java } : {}),
        ...(golang.length ? { golang } : {}),
      },
    };
  }
  if (rule.payloadCollection) {
    const pc = rule.payloadCollection;
    return {
      ruleType: RuleType.PayloadCollection,
      payloadCollection: {
        collectHttpRequest: !!pc.httpRequest,
        collectHttpResponse: !!pc.httpResponse,
        collectDbQuery: !!pc.dbQuery,
        collectMessaging: !!pc.messaging,
      },
    };
  }
  if (rule.headersCollection) {
    // `headerKeys` is a required, non-empty list on VM; drop blanks and reject
    // an otherwise-empty rule with a clear message rather than a server error.
    const headerKeys = (rule.headersCollection.headerKeys ?? []).map((k: string | null | undefined) => stripEmpty(k)).filter((k: string | undefined): k is string => !!k);
    if (headerKeys.length === 0) throw new Error('A "Headers Collection" rule requires at least one header key.');
    return { ruleType: RuleType.HeadersCollection, HeadersCollection: { headerKeys } };
  }
  // Default to CodeAttributes (the remaining kit rule type) when no other config
  // branch is present.
  const ca = rule.codeAttributes;
  return {
    ruleType: RuleType.CodeAttributes,
    codeAttributes: {
      collectColumn: !!ca?.column,
      collectFilePath: !!ca?.filePath,
      collectFunction: !!ca?.function,
      collectLineNumber: !!ca?.lineNumber,
      collectNamespace: !!ca?.namespace,
      collectStacktrace: !!ca?.stacktrace,
    },
  };
};

export const vmCreateInstrumentationRuleVariables = (vars: CreateInstrumentationRuleVars, ctx?: OperationContext): Record<string, unknown> => {
  assertAgentCanWriteActionsAndRules(ctx);
  const rule = vars.instrumentationRule;
  assertVmSupportedRuleFields(rule);
  // VM requires a non-empty resource `name` (it doubles as the id); generate one
  // when the kit form leaves the rule name blank. `enabled` is required by the
  // schema (the kit models the inverse `disabled`); `notes` is optional.
  const notes = stripEmpty(rule.notes);
  const input: CreateInstrumentationRuleInput = {
    name: stripEmpty(rule.ruleName) ?? generateId(),
    enabled: !rule.disabled,
    ...(notes ? { notes } : {}),
    ...buildVmRuleConfig(rule),
  };
  return { input };
};

export const vmUpdateInstrumentationRuleVariables = (vars: UpdateInstrumentationRuleVars, ctx?: OperationContext): Record<string, unknown> => {
  assertAgentCanWriteActionsAndRules(ctx);
  const rule = vars.instrumentationRule;
  assertVmSupportedRuleFields(rule, { ruleId: vars.ruleId });
  const notes = stripEmpty(rule.notes);
  const input: UpdateInstrumentationRuleInput = {
    name: vars.ruleId,
    enabled: !rule.disabled,
    ...(notes ? { notes } : {}),
    ...buildVmRuleConfig(rule),
  };
  return { input };
};

export const vmDeleteInstrumentationRuleVariables = (vars: DeleteInstrumentationRuleVars, ctx?: OperationContext): Record<string, unknown> => {
  assertAgentCanWriteActionsAndRules(ctx);
  return { input: { name: vars.ruleId } };
};

// ----- mutation result mappers -----

export const vmCreateInstrumentationRuleResult = (data: CreateInstrumentationRuleMutation | null | undefined): CreateInstrumentationRuleResult | undefined => {
  const created = data?.createInstrumentationRule;
  if (!created) return undefined;
  return { createInstrumentationRule: mapVmRuleToInstrumentationRule(created) };
};

export const vmUpdateInstrumentationRuleResult = (data: UpdateInstrumentationRuleMutation | null | undefined): UpdateInstrumentationRuleResult | undefined => {
  const updated = data?.updateInstrumentationRule;
  if (!updated) return undefined;
  return { updateInstrumentationRule: mapVmRuleToInstrumentationRule(updated) };
};

export const vmDeleteInstrumentationRuleResult = (data: DeleteInstrumentationRuleMutation | null | undefined): DeleteInstrumentationRuleResult | undefined => ({
  deleteInstrumentationRule: !!data?.deleteInstrumentationRule?.deleted,
});
