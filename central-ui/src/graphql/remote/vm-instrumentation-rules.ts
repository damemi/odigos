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
} from '@odigos/ui-kit/contexts/odigos-api';
import { stripEmpty } from './utils';

// The agents-api instrumentation-rule surface (v0.12.0+) is a discriminated
// union (`CodeAttributesRule` | `CustomInstrumentationRule` |
// `HeadersCollectionRule` | `PayloadCollectionRule`) keyed by `ruleType`, with
// a per-type `config`. central-ui's domain model (the ui-kit `InstrumentationRule`)
// is a single flat shape carrying one optional config object per rule type. The
// VM surface is also narrower than k8s: rules are global (no `sourceScopes` /
// `workloads`), carry no `notes`/`disabled`/`profileName`, and `payloadCollection`
// is a set of booleans rather than per-signal config objects. These helpers
// translate between the two surfaces (resolved from the version-pinned dialect
// documents), filling the VM-absent fields with neutral defaults.

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
    notes: '',
    disabled: false,
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
    default:
      return base;
  }
};

// `GetInstrumentationRules` envelope (`{ instrumentationRules: [...] }`) ->
// `GET_INSTRUMENTATION_RULES` result.
export const mapVmInstrumentationRules = (data: GetInstrumentationRulesQuery | null | undefined): GetInstrumentationRulesData => {
  const rules = data?.instrumentationRules ?? [];
  return { instrumentationRules: rules.map(mapVmRuleToInstrumentationRule) };
};

// ----- mutation variable mappers (-> agents-api `{ input }`) -----

// The kit's wire input (`InstrumentationRuleInputWire`) carries the spec-shape
// config objects but NOT the rule `type` (it's omitted from the form data), so
// the agents-api `ruleType` + matching config branch are inferred from whichever
// config object the form populated. The VM `CreateInstrumentationRuleInput` /
// `UpdateInstrumentationRuleInput` share the same config-branch fields, so a
// single builder serves both.
type VmRuleConfigInput = Pick<CreateInstrumentationRuleInput, 'ruleType' | 'codeAttributes' | 'HeadersCollection' | 'payloadCollection' | 'customInstrumentation'>;

type VmRuleInput = CreateInstrumentationRuleVars['instrumentationRule'];

const payloadKindHasAdvancedOptions = (kind: { mimeTypes?: string[] | null; maxPayloadLength?: number | null; dropPartialPayloads?: boolean | null } | null | undefined): boolean =>
  !!kind && ((kind.mimeTypes?.length ?? 0) > 0 || kind.maxPayloadLength != null || kind.dropPartialPayloads === true);

// The VM agents-api rule schema is far narrower than the kit's k8s rule model:
// `CreateInstrumentationRuleInput`/`UpdateInstrumentationRuleInput` only carry
// `name`, `ruleType`, and the per-type config branch. Every other field the kit
// form can set (disabled, notes, source scopes, workloads, conditions, a mutable
// display name, payload-collection advanced options, ...) has NO VM equivalent,
// so it would be silently dropped here and never reflected on read. Silently
// losing them is dangerous (e.g. a "disabled" rule stays active), so reject the
// mutation up front with a clear, itemized error instead of dropping data. The
// root `withGuardedRuns` wrapper turns this throw into a normal error toast.
const assertVmSupportedRuleFields = (rule: VmRuleInput, opts?: { ruleId?: string }): void => {
  const unsupported: string[] = [];

  if (rule.disabled) unsupported.push('disabling a rule (VM rules are always active)');
  if (stripEmpty(rule.notes)) unsupported.push('notes');
  if (opts?.ruleId && stripEmpty(rule.ruleName) && rule.ruleName !== opts.ruleId) unsupported.push('renaming a rule');
  if ((rule.sourcesScopes?.length ?? 0) > 0) unsupported.push('source scopes');
  if ((rule.workloads?.length ?? 0) > 0) unsupported.push('workloads');
  if ((rule.instrumentationLibraries?.length ?? 0) > 0) unsupported.push('instrumentation libraries');
  if ((rule.conditions?.length ?? 0) > 0) unsupported.push('conditions');
  const pc = rule.payloadCollection;
  if (pc && [pc.httpRequest, pc.httpResponse, pc.dbQuery, pc.messaging].some(payloadKindHasAdvancedOptions)) {
    unsupported.push('payload collection advanced options (mime types / max length / drop partial payloads)');
  }

  if (unsupported.length > 0) {
    throw new Error(`The VM agent does not support the following on instrumentation rules: ${unsupported.join(', ')}. These would be silently ignored — remove them, or manage this rule on a Kubernetes platform.`);
  }
};

const buildVmRuleConfig = (rule: CreateInstrumentationRuleVars['instrumentationRule']): VmRuleConfigInput => {
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

export const vmCreateInstrumentationRuleVariables = (vars: CreateInstrumentationRuleVars): Record<string, unknown> => {
  assertVmSupportedRuleFields(vars.instrumentationRule);
  // VM requires a non-empty resource `name` (it doubles as the id); generate one
  // when the kit form leaves the rule name blank.
  const input: CreateInstrumentationRuleInput = { name: stripEmpty(vars.instrumentationRule.ruleName) ?? generateId(), ...buildVmRuleConfig(vars.instrumentationRule) };
  return { input };
};

export const vmUpdateInstrumentationRuleVariables = (vars: UpdateInstrumentationRuleVars): Record<string, unknown> => {
  assertVmSupportedRuleFields(vars.instrumentationRule, { ruleId: vars.ruleId });
  const input: UpdateInstrumentationRuleInput = { name: vars.ruleId, ...buildVmRuleConfig(vars.instrumentationRule) };
  return { input };
};

export const vmDeleteInstrumentationRuleVariables = (vars: DeleteInstrumentationRuleVars): Record<string, unknown> => ({ input: { name: vars.ruleId } });

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
