import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { createRequire } from 'node:module';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const load = (file, dependencies = {}) => {
  const js = ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const mod = { exports: {} };
  new Function('require', 'module', 'exports', js)((id) => dependencies[id] ?? require(id), mod, mod.exports);
  return mod.exports;
};

const reports = load('../src/graphql/remote/managed-collector.ts');
const utils = load('../src/graphql/remote/utils.ts');
const destinations = load('../src/graphql/remote/vm-destinations.ts', {
  '@/graphql/remote/vm-dialect': { dialectVersion: (ctx) => ctx.version },
  '@/graphql/remote/utils': utils,
});

test('report mapper keeps manual authority, cutover issues and placement', () => {
  const report = reports.mapManagedCollectorReport({
    region: 'eu-west-1', networkId: 'vpc-1', status: 'AwaitingCustomer',
    desired: { cpu: '1024', memory: '2048', providerOverrides: { clusterRef: 'cluster-1' } },
    authority: { mode: 'manual', mutable: true, deletable: false },
    current: { firstReadyAt: null },
    issues: [{ code: 'CollectorRolloutInProgress', severity: 'info', message: '2 workloads routed' }],
  });
  assert.equal(report.authority.mode, 'manual');
  assert.equal(report.desired.providerOverrides.clusterRef, 'cluster-1');
  assert.equal(report.issues[0].message, '2 workloads routed');
  assert.equal(reports.mapManagedCollectorReport({ region: 'x' }), undefined);
});

test('managed destination sends region outside config and maps it back', () => {
  const vars = destinations.vmCreateDestinationVariables({ destination: {
    name: 'regional', type: 'odigos-managed-collector', fields: [{ key: 'REGION', value: ' eu-west-1 ' }, { key: 'X', value: 'y' }],
    exportedSignals: { traces: true, metrics: false, logs: false, profiles: false },
  } }, { version: '0.25.0' });
  assert.equal(vars.input.region, 'eu-west-1');
  assert.deepEqual(vars.input.config, { X: 'y' });
  const mapped = destinations.mapVmDestinations({ destinations: [{ name: 'regional', type: 'odigos-managed-collector', region: 'eu-west-1', config: { X: 'y' }, signals: ['TRACES'] }] });
  assert.match(mapped.destinations[0].fields, /"REGION":"eu-west-1"/);
});

test('schema version gates destination region and disabled fields', () => {
  assert.equal(utils.supportsDestinationRegion('0.15.0'), false);
  assert.equal(utils.supportsDestinationRegion('0.16.0'), true);
  assert.equal(utils.supportsDestinationDisabled('0.23.0'), false);
  assert.equal(utils.supportsDestinationDisabled('0.25.0'), true);
});
