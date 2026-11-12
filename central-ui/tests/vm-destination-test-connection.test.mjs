import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const load = (file, dependencies = {}) => {
  const js = ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const cjsModule = { exports: {} };
  new Function('require', 'module', 'exports', js)((id) => dependencies[id] ?? require(id), cjsModule, cjsModule.exports);
  return cjsModule.exports;
};

const utils = load('../src/graphql/remote/utils.ts', { '@odigos/ui-kit/types': {} });
const { dialectVersion } = load('../src/graphql/remote/vm-dialect.ts', {
  '@odigos/vmagent-api-apollo-client/registry': { LATEST_DIALECT_VERSION: '0.36.0', resolveDialect: () => ({}) },
  '@/graphql/remote/utils': utils,
});
const { mapVmDestinationTypesToCategories, patchVmDestinationTypesQuery } = load('../src/graphql/remote/vm-destinations.ts', {
  '@/graphql/remote/vm-dialect': { dialectVersion },
  '@/graphql/remote/utils': utils,
});

const typesQuery = `query GetDestinationTypes {
  destinationTypes {
    ...DestinationTypeFields
  }
}

fragment DestinationTypeFields on DestinationType {
  type
  displayName
}`;

const catalogItems = (types) =>
  mapVmDestinationTypesToCategories({ destinationTypes: types }).destinationCategories.categories.flatMap((c) => c.items);
const type = (name, extra = {}) => ({
  type: name,
  displayName: name,
  imageUrl: '',
  supportedSignals: { traces: true, metrics: true, logs: true, profiles: false },
  fields: [],
  ...extra,
});

test('selects testConnectionSupported only on schemas that serve it', () => {
  assert.match(patchVmDestinationTypesQuery(typesQuery, { version: '1.0.0', schemaVersion: '0.37.0' }), /testConnectionSupported/);
  assert.match(patchVmDestinationTypesQuery(typesQuery, { version: '1.0.0', schemaVersion: 'v0.40.1' }), /testConnectionSupported/);
  assert.equal(patchVmDestinationTypesQuery(typesQuery, { version: '1.0.0', schemaVersion: '0.36.0' }), typesQuery);
  assert.equal(patchVmDestinationTypesQuery(typesQuery, { version: '0.30.0' }), typesQuery);
});

test('local development agents never get the unknown field', () => {
  assert.equal(patchVmDestinationTypesQuery(typesQuery, { version: 'development' }), typesQuery);
  assert.equal(patchVmDestinationTypesQuery(typesQuery, { version: '(devel)', schemaVersion: 'UNKNOWN-VERSION' }), typesQuery);
});

test('does not duplicate a selection the dialect already carries', () => {
  const native = typesQuery.replace('displayName', 'displayName\n  testConnectionSupported');
  const patched = patchVmDestinationTypesQuery(native, { version: '1.0.0', schemaVersion: '0.37.0' });
  assert.equal(patched.match(/testConnectionSupported/g).length, 1);
});

test('maps the per-type flag from the agent', () => {
  const items = catalogItems([type('datadog', { testConnectionSupported: true }), type('s3', { testConnectionSupported: false })]);
  assert.equal(items.find((i) => i.type === 'datadog').testConnectionSupported, true);
  assert.equal(items.find((i) => i.type === 's3').testConnectionSupported, false);
});

test('treats a missing flag as unsupported (older agents)', () => {
  assert.equal(catalogItems([type('datadog')])[0].testConnectionSupported, false);
  assert.equal(catalogItems([type('datadog', { testConnectionSupported: null })])[0].testConnectionSupported, false);
});

test('managed collector is never testable', () => {
  assert.equal(catalogItems([type('odigos-managed-collector', { testConnectionSupported: true })])[0].testConnectionSupported, false);
});
