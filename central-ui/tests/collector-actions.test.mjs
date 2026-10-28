import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const keys = ['UrlTemplatizationRulesGroups', 'ProcessorType', 'ProcessorName', 'ProcessorConfig', 'SpanMetricsDisabled', 'Interval', 'MetricsExpiration', 'AdditionalDimensions', 'HistogramDisabled', 'ExplicitHistogramBuckets', 'ResourceMetricsKeyAttributes', 'IncludedProcessInDimensions', 'ExcludedResourceAttributes', 'TemplatizeLiterals', 'RemovePostgresCastOperator', 'ExtractAttribute', 'AttributeNamesToDelete', 'Renames'];
const kit = {
  ActionType: { URLTemplatization: 'URLTemplatization', ProcessorAddition: 'ProcessorAddition', SpanMetrics: 'SpanMetrics', DbQueryTemplatization: 'DbQueryTemplatization', InferDbAttributes: 'InferDbAttributes', ExtractAttribute: 'ExtractAttribute', DeleteAttributes: 'DeleteAttribute', RenameAttributes: 'RenameAttribute', PiiMasking: 'PiiMasking' },
  ActionKeyTypes: Object.fromEntries(keys.map((name) => [name, name[0].toLowerCase() + name.slice(1)])),
  PlatformType: { Connector: 'connector', Vm: 'vm' },
  SignalType: { Logs: 'LOGS', Metrics: 'METRICS', Traces: 'TRACES', Profiles: 'PROFILES' },
  FieldTypes: { Dropdown: 'dropdown', Input: 'input', TextArea: 'textarea', Toggle: 'toggle', MultiInput: 'multiInput' },
};
const api = {
  ActionType: Object.fromEntries(['UrlTemplatization', 'ProcessorAddition', 'SpanMetrics', 'DbQueryTemplatization', 'InferDbAttributes', 'ExtractAttribute', 'DeleteAttribute', 'RenameAttribute'].map((name) => [name, name[0].toLowerCase() + name.slice(1)])),
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
const actions = load('../src/graphql/remote/vm-actions.ts', {
  '@odigos/vmagent-api-apollo-client': api,
  '@odigos/ui-kit/types': kit,
  '@odigos/ui-kit/functions': { generateId: () => 'generated-action' },
  './utils': utils,
  './vm-dialect': { dialectVersion: (ctx) => ctx.schemaVersion ?? ctx.version },
});
const catalog = load('../src/graphql/remote/vm-action-types.ts', { '@odigos/ui-kit/types': kit });

const cases = [
  ['DbQueryTemplatization', api.ActionType.DbQueryTemplatization, kit.ActionType.DbQueryTemplatization, 'dbQueryTemplatization', { templatizeLiterals: true, removePostgresCastOperator: false }],
  ['InferDbAttributes', api.ActionType.InferDbAttributes, kit.ActionType.InferDbAttributes, null, undefined],
  ['ExtractAttribute', api.ActionType.ExtractAttribute, kit.ActionType.ExtractAttribute, 'extractAttribute', { extractions: [{ targetAttributeName: 'tenant', lookupKey: 'tenants', dataFormat: 'resource_path' }] }],
  ['DeleteAttribute', api.ActionType.DeleteAttribute, kit.ActionType.DeleteAttributes, 'deleteAttribute', { attributeNamesToDelete: ['user.email'] }],
  ['RenameAttribute', api.ActionType.RenameAttribute, kit.ActionType.RenameAttributes, 'renameAttribute', { renames: [{ from: 'old', to: 'new' }] }],
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
  assert.ok(!options.some((option) => option.type === kit.ActionType.PiiMasking));
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
