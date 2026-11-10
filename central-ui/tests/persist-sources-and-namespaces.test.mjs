import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import ts from 'typescript';

const require = createRequire(import.meta.url);
// Mirrors the kit's trimVersion (major * 10000 + minor); its bundle needs a browser to load.
const trimVersion = (v) => {
  const [major = 0, minor = 0] = String(v)
    .replace('v', '')
    .split('-')[0]
    .split('.')
    .map((s) => parseInt(s, 10) || 0);
  return major * 10000 + minor;
};
const kit = { PlatformType: { K8s: 'k8s', Vm: 'vm' }, StatusType: { Error: 'error' } };
const load = (file, dependencies = {}) => {
  const js = ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const cjsModule = { exports: {} };
  new Function('require', 'module', 'exports', js)((id) => dependencies[id] ?? require(id), cjsModule, cjsModule.exports);
  return cjsModule.exports;
};
const { getVersionedQuery } = load('../src/utils/functions/common.ts', {
  '@odigos/ui-kit/functions': { trimVersion },
  '@odigos/ui-kit/store': {},
  '@odigos/ui-kit/components': { TagVariants: {} },
  '@odigos/ui-kit/types': kit,
  '@/types': { Role: {} },
});
const { targetProxyContext, buildProxyVersions } = load('../src/lib/target-proxy-context.ts');
const { PERSIST_SOURCES_AND_NAMESPACES } = load('../src/graphql/mutations/remote/sources.ts', { '@odigos/ui-kit/types': kit });

// Mirrors the adapter's `withTargetProxyVersion(wrapVersioned(..., { unsupported: true }))`:
// resolve the addressed proxy's own version, refuse VM agents and connectors, then floor the
// K8s bucket on that version.
const canRun = (ctx) => {
  const target = targetProxyContext(ctx);
  if (!target) return false;
  if (target.platformType !== kit.PlatformType.K8s) return false;
  return !!getVersionedQuery(PERSIST_SOURCES_AND_NAMESPACES, target.platformType, target.schemaVersion ?? target.version);
};

const connections = [
  { id: 'new', type: 'k8s', odigosVersion: 'v1.40.0', graphqlSchemaVersion: null },
  { id: 'old', type: 'k8s', odigosVersion: 'v1.39.0', graphqlSchemaVersion: null },
  { id: 'vm', type: 'vm', odigosVersion: 'v1.40.0', graphqlSchemaVersion: 'v0.32.0' },
  { id: 'connector', type: 'connector', odigosVersion: 'v1.40.0', graphqlSchemaVersion: 'v0.32.0' },
  { id: 'pending', type: 'k8s', odigosVersion: '', graphqlSchemaVersion: null },
];
const proxyVersions = buildProxyVersions(connections);
const onNew = { platformType: 'k8s', tier: 'onprem', version: 'v1.40.0', proxyID: 'new', proxyVersions };
const onOld = { platformType: 'k8s', tier: 'onprem', version: 'v1.39.0', proxyID: 'old', proxyVersions };

test('mutation sends sources with namespaces in one request', () => {
  const document = getVersionedQuery(PERSIST_SOURCES_AND_NAMESPACES, 'k8s', 'v1.40.0');
  assert.match(document, /persistK8sSources\(sources: \$sources, namespaces: \$namespaces\)/);
});

test('supported cluster can create disabled sources', () => {
  assert.equal(canRun(onNew), true);
  for (const version of ['v1.40.0-rc1', 'v1.40.2', 'v1.41.0']) {
    assert.equal(canRun({ ...onNew, proxyVersions: buildProxyVersions([{ id: 'new', type: 'k8s', odigosVersion: version }]) }), true, version);
  }
});

test('older cluster refuses, even when the kit overlays it on a supported ambient context', () => {
  assert.equal(canRun(onOld), false);
  assert.equal(canRun({ ...onNew, proxyID: 'old' }), false);
});

test('supported cluster is allowed when the ambient context is an older one', () => {
  assert.equal(canRun({ ...onOld, proxyID: 'new' }), true);
});

test('multi-cluster save decides per cluster, VM agents and connectors never', () => {
  const decisions = ['new', 'old', 'vm', 'connector'].map((proxyID) => [proxyID, canRun({ ...onNew, proxyID })]);
  assert.deepEqual(decisions, [
    ['new', true],
    ['old', false],
    ['vm', false],
    ['connector', false],
  ]);
});

test('unknown version refuses instead of borrowing the ambient version', () => {
  assert.equal(canRun({ ...onNew, proxyID: 'missing' }), false);
  assert.equal(canRun({ ...onNew, proxyID: 'pending' }), false);
  assert.equal(canRun({ ...onNew, proxyVersions: undefined }), false);
});
