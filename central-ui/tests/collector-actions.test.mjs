import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const keys = ['UrlTemplatizationRulesGroups', 'ProcessorType', 'ProcessorName', 'ProcessorConfig', 'SpanMetricsDisabled', 'Interval', 'MetricsExpiration', 'AdditionalDimensions', 'HistogramDisabled', 'ExplicitHistogramBuckets', 'ResourceMetricsKeyAttributes', 'IncludedProcessInDimensions', 'ExcludedResourceAttributes', 'TemplatizeLiterals', 'RemovePostgresCastOperator', 'ExtractAttribute', 'AttributeNamesToDelete', 'Renames', 'PiiCategories'];
const kit = {
  ActionType: { URLTemplatization: 'URLTemplatization', ProcessorAddition: 'ProcessorAddition', SpanMetrics: 'SpanMetrics', DbQueryTemplatization: 'DbQueryTemplatization', InferDbAttributes: 'InferDbAttributes', ExtractAttribute: 'ExtractAttribute', DeleteAttributes: 'DeleteAttribute', RenameAttributes: 'RenameAttribute', PiiMasking: 'PiiMasking' },
  ActionKeyTypes: Object.fromEntries(keys.map((name) => [name, name[0].toLowerCase() + name.slice(1)])),
  PlatformType: { Connector: 'connector', Vm: 'vm' },
  SignalType: { Logs: 'LOGS', Metrics: 'METRICS', Traces: 'TRACES', Profiles: 'PROFILES' },
  FieldTypes: { Dropdown: 'dropdown', Input: 'input', TextArea: 'textarea', Toggle: 'toggle', MultiInput: 'multiInput' },
};
const api = {
  ActionType: Object.fromEntries(['UrlTemplatization', 'ProcessorAddition', 'SpanMetrics', 'DbQueryTemplatization', 'InferDbAttributes', 'ExtractAttribute', 'DeleteAttribute', 'RenameAttribute', 'PiiMasking'].map((name) => [name, name[0].toLowerCase() + name.slice(1)])),
  TelemetrySignal: kit.SignalType,
};
const load = (file, dependencies = {}) => {
  const js = ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const mod = { exports: {} };
  new Function('require', 'module', 'exports', js)((id) => dependencies[id] ?? require(id), mod, mod.exports);
  return mod.exports;
};
const utils = load('../src/graphql/remote/utils.ts');
const dialect = load('../src/graphql/remote/vm-dialect.ts', {
  graphql: { print: () => '' },
  '@odigos/vmagent-api-apollo-client/registry': { LATEST_DIALECT_VERSION: 'v0.36.0', resolveDialect: () => ({}) },
  '@/graphql/remote/utils': utils,
});
const actions = load('../src/graphql/remote/vm-actions.ts', {
  '@odigos/vmagent-api-apollo-client': api,
  '@odigos/ui-kit/types': kit,
  '@odigos/ui-kit/functions': { generateId: () => 'generated-action' },
  './utils': utils,
  './vm-dialect': dialect,
});
const catalog = load('../src/graphql/remote/vm-action-types.ts', { '@odigos/ui-kit/types': kit });
const rules = load('../src/graphql/remote/vm-instrumentation-rules.ts', {
  '@odigos/vmagent-api-apollo-client': { RuleType: { CodeAttributes: 'CodeAttributes', HeadersCollection: 'HeadersCollection', PayloadCollection: 'PayloadCollection', CustomInstrumentation: 'CustomInstrumentation', KernelTraces: 'KernelTraces', Sampling: 'Sampling' } },
  '@odigos/ui-kit/types': { InstrumentationRuleType: { CodeAttributes: 'CodeAttributes', HeadersCollection: 'HeadersCollection', PayloadCollection: 'PayloadCollection', CustomInstrumentation: 'CustomInstrumentation', KernelTraces: 'KernelTraces', UnknownType: 'UnknownType' }, JavaCustomProbe: class {}, GolangCustomProbe: class {} },
  '@odigos/ui-kit/functions': { generateId: () => 'generated-rule' },
  './utils': utils,
  './vm-dialect': dialect,
});
const ruleTypes = load('../src/graphql/remote/vm-rule-types.ts', { '@odigos/ui-kit/types': { InstrumentationRuleType: { CodeAttributes: 'CodeAttributes', CustomInstrumentation: 'CustomInstrumentation', HeadersCollection: 'HeadersCollection', PayloadCollection: 'PayloadCollection', KernelTraces: 'KernelTraces' } } });

const cases = [
  ['DbQueryTemplatization', api.ActionType.DbQueryTemplatization, kit.ActionType.DbQueryTemplatization, 'dbQueryTemplatization', { templatizeLiterals: true, removePostgresCastOperator: false }],
  ['InferDbAttributes', api.ActionType.InferDbAttributes, kit.ActionType.InferDbAttributes, null, undefined],
  ['ExtractAttribute', api.ActionType.ExtractAttribute, kit.ActionType.ExtractAttribute, 'extractAttribute', { extractions: [{ targetAttributeName: 'tenant', lookupKey: 'tenants', dataFormat: 'resource_path' }] }],
  ['DeleteAttribute', api.ActionType.DeleteAttribute, kit.ActionType.DeleteAttributes, 'deleteAttribute', { attributeNamesToDelete: ['user.email'] }],
  ['RenameAttribute', api.ActionType.RenameAttribute, kit.ActionType.RenameAttributes, 'renameAttribute', { renames: [{ from: 'old', to: 'new' }] }],
  ['PiiMasking', api.ActionType.PiiMasking, kit.ActionType.PiiMasking, 'piiMasking', { piiCategories: ['CREDIT_CARD', 'EMAIL'], customFormatMaskings: [{ dataFormat: 'sql', lookupKey: 'ssn' }], customRegexMaskings: [{ regex: 'id=([^&]+)' }] }],
];

for (const [name, actionType, kitType, branch, config] of cases) {
  test(`${name} survives read, create and update without changing its config`, () => {
    const raw = { __typename: `${name}Action`, name: 'id', actionName: 'My action', region: 'us-east-1', actionType, disabled: false, signals: ['TRACES'], config };
    const mapped = actions.mapVmActionToAction(raw);
    assert.equal(mapped.type, kitType);
    assert.equal(mapped.region, 'us-east-1');
    for (const result of [actions.vmCreateActionVariables({ action: mapped }), actions.vmUpdateActionVariables({ id: 'id', action: mapped })]) {
      assert.equal(result.input.actionType, actionType);
      assert.equal(result.input.region, 'us-east-1');
      if (branch) assert.deepEqual(result.input[branch], config);
    }
    assert.equal(actions.mapVmActions({ actions: [raw] }).actions.length, 1);
  });
}

test('new catalog types are not filtered out or given Kubernetes-only fields', () => {
  const options = catalog.mapVmActionTypesToOptions({ actionTypes: cases.map(([, type]) => ({ type, displayName: type, description: '', allowedSignals: ['TRACES'], docsUrl: '', fields: [] })) }).actionTypes;
  for (const [, , type] of cases) assert.ok(options.some((option) => option.type === type));
  assert.ok(!options.some((option) => option.type === kit.ActionType.ProcessorAddition || option.type === kit.ActionType.SpanMetrics));
});

test('processor and span metrics fallbacks are appended for Cloud Connectors only', () => {
  const data = { actionTypes: [{ type: 'piiMasking', displayName: 'PII Masking', description: '', allowedSignals: ['TRACES'], docsUrl: '', fields: [] }] };
  const types = (ctx) => catalog.mapVmActionTypesToOptions(data, ctx).actionTypes.map((option) => option.type);
  assert.deepEqual(types({ platformType: kit.PlatformType.Vm }), [kit.ActionType.PiiMasking]);
  assert.deepEqual(types({ platformType: kit.PlatformType.Connector }), [kit.ActionType.PiiMasking, kit.ActionType.ProcessorAddition, kit.ActionType.SpanMetrics]);
});

test('region is selected for cloud connectors, never for released VM schemas', () => {
  const query = 'query GetActions { actions { name actionType } }';
  assert.match(actions.patchVmActionsQuery(query, { platformType: kit.PlatformType.Connector, version: '0.31.0' }), /region/);
  assert.doesNotMatch(actions.patchVmActionsQuery(query, { platformType: kit.PlatformType.Vm, version: '0.31.0' }), /region/);
});

test('unsupported Kubernetes scopes and invalid DB/attribute configs are rejected', () => {
  const action = { type: kit.ActionType.InferDbAttributes, signals: ['TRACES'], fields: { scopes: { names: ['source'] } } };
  assert.throws(() => actions.vmCreateActionVariables({ action }), /scoping/);
  assert.throws(() => actions.vmCreateActionVariables({ action: { ...action, type: kit.ActionType.DbQueryTemplatization, fields: { templatizeLiterals: false, removePostgresCastOperator: false } } }), /Enable/);
  assert.throws(() => actions.vmCreateActionVariables({ action: { ...action, type: kit.ActionType.RenameAttributes, fields: { renames: { old: 'old' } } } }), /different/);
});

test('PII custom maskings round trip through form fields: read, create, edit and remove', () => {
  const custom = { customFormatMaskings: [{ dataFormat: 'json', lookupKey: 'user' }], customRegexMaskings: [{ regex: '\\d{4}' }] };
  const raw = { __typename: 'PiiMaskingAction', name: 'pii-1', actionName: 'PII', actionType: api.ActionType.PiiMasking, disabled: false, signals: ['TRACES'], config: { piiCategories: ['EMAIL'], ...custom } };
  const mapped = actions.mapVmActionToAction(raw);
  assert.deepEqual(mapped.fields, { piiCategories: ['EMAIL'], ...custom });
  assert.deepEqual(actions.vmCreateActionVariables({ action: mapped }).input.piiMasking, { piiCategories: ['EMAIL'], ...custom });
  const removed = { ...mapped, fields: { piiCategories: ['EMAIL'], customFormatMaskings: [], customRegexMaskings: [] } };
  assert.deepEqual(actions.vmUpdateActionVariables({ id: 'pii-1', action: removed }).input.piiMasking, { piiCategories: ['EMAIL'], customFormatMaskings: [], customRegexMaskings: [] });
});

test('a regex-only PII rule is accepted and an empty one is rejected', () => {
  const action = { type: kit.ActionType.PiiMasking, signals: ['TRACES'], fields: { piiCategories: [], customRegexMaskings: [{ regex: 'id=([^&]+)' }, { regex: ' ' }] } };
  assert.deepEqual(actions.vmCreateActionVariables({ action }).input.piiMasking, { piiCategories: [], customFormatMaskings: [], customRegexMaskings: [{ regex: 'id=([^&]+)' }] });
  assert.throws(() => actions.vmCreateActionVariables({ action: { ...action, fields: { piiCategories: [], customFormatMaskings: [{ dataFormat: 'json', lookupKey: '' }] } } }), /at least one PII category/);
});

test('agents older than schema v0.12 are read from computePlatform and refuse writes with an upgrade message', () => {
  const legacy = { computePlatform: { actions: [{ name: 'a1', spec: { actionName: 'Legacy', disabled: false, signals: ['TRACES'], type: 'DeleteAttribute', config: { attributeNamesToDelete: ['x'] } } }] } };
  const [action] = actions.mapVmActions(legacy).actions;
  assert.equal(action.type, kit.ActionType.DeleteAttributes);
  assert.deepEqual(action.fields.attributeNamesToDelete, ['x']);
  const unknown = { computePlatform: { actions: [legacy.computePlatform.actions[0], { name: 'a2', spec: { actionName: 'Future', type: 'SomethingNew', config: {} } }] } };
  assert.equal(actions.mapVmActions(unknown).actions.length, 1);
  const old = { platformType: kit.PlatformType.Vm, schemaVersion: '0.11.0' };
  const current = { platformType: kit.PlatformType.Vm, schemaVersion: '0.36.0' };
  assert.throws(() => actions.vmCreateActionVariables({ action }, old), /Upgrade the agent/);
  assert.throws(() => actions.vmDeleteActionVariables({ id: 'a1' }, old), /Upgrade the agent/);
  assert.doesNotThrow(() => actions.vmDeleteActionVariables({ id: 'a1' }, current));
  assert.throws(() => rules.vmDeleteInstrumentationRuleVariables({ ruleId: 'r' }, old), /Upgrade the agent/);
});

test('Kernel Traces rules round trip, treating a present operation as checked, and Sampling stays out of the list', () => {
  const config = { filesystem: { enabled: true }, subprocesses: { recordCommandArgs: false }, cpuDelay: { enabled: true } };
  const raw = { __typename: 'KernelTracesRule', name: 'kt', ruleType: 'KernelTraces', enabled: true, notes: null, config };
  const sampling = { __typename: 'SamplingRule', name: 's', ruleType: 'Sampling', enabled: true, config: {} };
  const { instrumentationRules } = rules.mapVmInstrumentationRules({ instrumentationRules: [raw, sampling] });
  assert.equal(instrumentationRules.length, 1);
  const [rule] = instrumentationRules;
  assert.equal(rule.type, 'KernelTraces');
  assert.deepEqual(rule.kernelTraces, config);
  for (const { input } of [rules.vmCreateInstrumentationRuleVariables({ instrumentationRule: rule }), rules.vmUpdateInstrumentationRuleVariables({ ruleId: 'kt', instrumentationRule: rule })]) {
    assert.equal(input.ruleType, 'KernelTraces');
    assert.deepEqual(input.kernelTraces, config);
  }
  // The kit form stores a checked operation as `{}` and omits an unchecked one.
  const form = { ruleName: 'kt', kernelTraces: { filesystem: {}, cpuDelay: {} } };
  assert.deepEqual(rules.vmCreateInstrumentationRuleVariables({ instrumentationRule: form }).input.kernelTraces, { filesystem: { enabled: true }, cpuDelay: { enabled: true } });
  const disabled = rules.mapVmInstrumentationRules({ instrumentationRules: [{ ...raw, config: { filesystem: { enabled: false }, subprocesses: {}, cpuDelay: { enabled: false } } }] }).instrumentationRules[0];
  assert.deepEqual(disabled.kernelTraces, { filesystem: null, subprocesses: { recordCommandArgs: false }, cpuDelay: null });
  const legacy = rules.mapVmInstrumentationRules({ computePlatform: { instrumentationRules: [{ name: 'c', type: 'CodeAttributes', config: { function: true } }] } });
  assert.equal(legacy.instrumentationRules[0].codeAttributes.function, true);
  assert.equal(legacy.instrumentationRules[0].mutable, false);
  assert.equal(rules.mapVmInstrumentationRules({ computePlatform: { instrumentationRules: [{ name: 'x', type: 'Mystery', config: {} }] } }).instrumentationRules.length, 0);
});

test('rule catalog maps Kernel Traces and drops Sampling', () => {
  const entry = (type) => ({ type, displayName: type, description: '', supportedLanguages: [], docsUrl: '', fields: [] });
  const result = ruleTypes.mapVmRuleTypesToOptions({ instrumentationRuleTypes: ['CodeAttributes', 'KernelTraces', 'Sampling'].map(entry) });
  assert.deepEqual(result.instrumentationRuleTypes.map((o) => o.type), ['CodeAttributes', 'KernelTraces']);
});
