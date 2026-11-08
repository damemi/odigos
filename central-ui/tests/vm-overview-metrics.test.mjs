import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { test } from 'node:test';
import { print } from 'graphql';
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
const metrics = load('../src/graphql/remote/vm-overview-metrics.ts', { '@/graphql/remote/vm-sources': { VM_NAMESPACE: 'default' } });
// The client's registry imports its snapshots without extensions (bundler-only), so
// read the frozen dialects straight from disk.
const snapshot = (version) => import(new URL(`../node_modules/@odigos/vmagent-api-apollo-client/dist/generated/${version}/graphql.js`, import.meta.url));

// GET_METRICS selects `OverviewMetricsDocument`; a dialect without it makes `canRun`
// false, so older agents skip the query instead of receiving the k8s one.
test('only dialects from v0.37.0 carry the overviewMetrics document', async () => {
  assert.match(print((await snapshot('v0_37_0')).OverviewMetricsDocument), /\boverviewMetrics \{/);
  for (const version of ['v0_6_0', 'v0_11_0', 'v0_12_0', 'v0_36_0']) assert.equal((await snapshot(version)).OverviewMetricsDocument, undefined, version);
});

test('maps sources onto VM workload ids and destinations onto their names', () => {
  const data = {
    overviewMetrics: {
      sources: [
        { name: 'frontend', kind: 'docker', region: null, totalDataSent: 4096, throughput: 512.5 },
        { name: 'orders', kind: 'aws.lambda', region: 'us-east-2', totalDataSent: 0, throughput: 0 },
      ],
      destinations: [{ name: 'debug', totalDataSent: 4096, throughput: 512.5 }],
    },
  };
  assert.deepEqual(metrics.mapVmOverviewMetrics(data), {
    getOverviewMetrics: {
      sources: [
        { namespace: 'default', name: 'frontend', kind: 'docker', throughput: 512.5 },
        { namespace: 'default', name: 'orders', kind: 'aws.lambda', region: 'us-east-2', throughput: 0 },
      ],
      destinations: [{ id: 'debug', throughput: 512.5 }],
    },
  });
});

test('a missing overviewMetrics payload yields no metrics, not zeros', () => {
  assert.equal(metrics.mapVmOverviewMetrics(null), undefined);
  assert.equal(metrics.mapVmOverviewMetrics({}), undefined);
  assert.deepEqual(metrics.mapVmOverviewMetrics({ overviewMetrics: { sources: null, destinations: null } }), { getOverviewMetrics: { sources: [], destinations: [] } });
});
