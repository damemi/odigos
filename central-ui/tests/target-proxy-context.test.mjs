import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import ts from 'typescript';

const source = readFileSync(new URL('../src/lib/target-proxy-context.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
const cjs = { exports: {} };
new Function('module', 'exports', js)(cjs, cjs.exports);
const { targetProxyContext, buildProxyVersions } = cjs.exports;

const connections = [
  { id: 'new', type: 'k8s', odigosVersion: 'v1.40.0', graphqlSchemaVersion: null },
  { id: 'old', type: 'k8s', odigosVersion: 'v1.39.0', graphqlSchemaVersion: null },
  { id: 'vm', type: 'vm', odigosVersion: 'v0.30.0', graphqlSchemaVersion: 'v0.32.0' },
  { id: 'pending', type: 'k8s', odigosVersion: '', graphqlSchemaVersion: null },
];
// Ambient context on the supported cluster's pages.
const ambient = { platformType: 'k8s', tier: 'onprem', version: 'v1.40.0', proxyID: 'new', proxyVersions: buildProxyVersions(connections) };

test('supported proxy keeps its own version', () => {
  const ctx = targetProxyContext(ambient);
  assert.equal(ctx.proxyID, 'new');
  assert.equal(ctx.version, 'v1.40.0');
});

test('per-cluster overlay uses the older proxy version, not the ambient one', () => {
  const ctx = targetProxyContext({ ...ambient, proxyID: 'old' });
  assert.equal(ctx.version, 'v1.39.0');
  assert.equal(ctx.platformType, 'k8s');
  assert.equal(ctx.schemaVersion, undefined);
});

test('multi-cluster save resolves each cluster separately', () => {
  const versions = ['new', 'old', 'vm'].map((proxyID) => {
    const ctx = targetProxyContext({ ...ambient, proxyID });
    return [proxyID, ctx.platformType, ctx.version, ctx.schemaVersion];
  });
  assert.deepEqual(versions, [
    ['new', 'k8s', 'v1.40.0', undefined],
    ['old', 'k8s', 'v1.39.0', undefined],
    ['vm', 'vm', 'v0.30.0', 'v0.32.0'],
  ]);
});

test('connections scope addresses the first selected connection', () => {
  const ctx = targetProxyContext({ ...ambient, proxyID: undefined, connectionIds: ['old', 'new'] });
  assert.equal(ctx.proxyID, 'old');
  assert.equal(ctx.version, 'v1.39.0');
});

test('unknown or unreported version refuses instead of borrowing the ambient version', () => {
  assert.equal(targetProxyContext({ ...ambient, proxyID: 'missing' }), undefined);
  assert.equal(targetProxyContext({ ...ambient, proxyID: 'pending' }), undefined);
  assert.equal(targetProxyContext({ ...ambient, proxyVersions: undefined }), undefined);
  assert.equal(targetProxyContext({ ...ambient, proxyID: undefined }), undefined);
});

test('selected proxy seeds the map until the connections list loads, then the list wins', () => {
  const selected = { proxyID: 'new', platformType: 'k8s', odigosVersion: 'v1.39.0' };
  assert.equal(buildProxyVersions([], selected).new.version, 'v1.39.0');
  assert.equal(buildProxyVersions(connections, selected).new.version, 'v1.40.0');
});
