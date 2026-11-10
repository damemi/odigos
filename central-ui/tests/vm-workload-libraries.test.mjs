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

const utils = load('../src/graphql/remote/utils.ts');
const dialect = { dialectVersion: (ctx) => ctx.schemaVersion ?? ctx.version };
const kit = { DesiredStateProgress: { Success: 'Success', Pending: 'Pending', Unsupported: 'Unsupported' }, ProgrammingLanguages: { Go: 'go', Java: 'java', Unknown: 'unknown' } };
const { mergeVmWorkloadReports } = load('../src/graphql/remote/vm-workload-reports.ts', {
  '@/graphql/remote/utils': utils,
  '@/graphql/remote/vm-dialect': dialect,
  '@odigos/ui-kit/types': kit,
});

const workloadFor = (name, kind) => ({ id: { namespace: '', name, kind, region: undefined } });
const reportFor = (name, kind, extra = {}) => ({ serviceName: name, kind, region: null, current: { state: 'instrumented' }, ...extra });
const merge = (name, kind, extra) => mergeVmWorkloadReports([workloadFor(name, kind)], [reportFor(name, kind, extra)])[0];

test('report libraries reach instrumentationReport.libraries for a kind without containers', () => {
  const merged = merge('billing', 'systemd', {
    libraries: [
      { name: 'net/http', type: 'instrumentation', healthy: true, message: null, reason: null },
      { name: 'database/sql', type: 'instrumentation', healthy: true },
    ],
  });
  assert.deepEqual(merged.instrumentationReport.libraries, [
    { name: 'net/http', healthy: true, message: null },
    { name: 'database/sql', healthy: true, message: null },
  ]);
  assert.equal(merged.containers, undefined);
});

test('an unhealthy library keeps its message', () => {
  const merged = merge('billing', 'process', {
    libraries: [
      { name: 'net/http', type: 'instrumentation', healthy: true },
      { name: 'google.golang.org/grpc', type: 'instrumentation', healthy: false, message: 'failed to attach uprobe: symbol not found', reason: 'AttachFailed' },
    ],
  });
  assert.deepEqual(merged.instrumentationReport.libraries[1], { name: 'google.golang.org/grpc', healthy: false, message: 'failed to attach uprobe: symbol not found' });
});

test('a report without the field leaves libraries unset instead of an empty list', () => {
  const merged = merge('billing', 'systemd', {});
  assert.equal('libraries' in merged.instrumentationReport, false);
  assert.equal(merged.instrumentationReport.libraries, undefined);
});

test('null and empty library lists also stay unset', () => {
  assert.equal('libraries' in merge('billing', 'systemd', { libraries: null }).instrumentationReport, false);
  assert.equal('libraries' in merge('billing', 'systemd', { libraries: [] }).instrumentationReport, false);
});

test('a container kind keeps its container rows and does not get workload-level libraries', () => {
  const merged = merge('web', 'docker', {
    details: { __typename: 'DockerWorkloadDetails', containers: [{ name: 'app', instrumented: true }] },
    libraries: [{ name: 'net/http', type: 'instrumentation', healthy: true }],
  });
  assert.deepEqual(merged.containers.map((c) => c.containerName), ['app']);
  assert.equal('libraries' in merged.instrumentationReport, false);
});
