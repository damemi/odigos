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
const kit = { PlatformType: { K8s: 'k8s', Vm: 'vm', Connector: 'connector' }, StatusType: { Error: 'error' } };
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
const { GET_LIVE_TRAFFIC_LEARNING } = load('../src/graphql/queries/remote/sources.ts', { '@odigos/ui-kit/types': kit });

// Workload containers serve `urlTemplatization` from odigos v1.39; asking an older proxy fails schema validation.
test('GET_LIVE_TRAFFIC_LEARNING resolves only on Kubernetes proxies from v1.39', () => {
  for (const version of ['v1.25.0', 'v1.38.4']) {
    assert.equal(getVersionedQuery(GET_LIVE_TRAFFIC_LEARNING, kit.PlatformType.K8s, version), undefined, version);
  }
  for (const version of ['v1.39.0-rc0', 'v1.39.0', 'v1.40.0']) {
    const selected = getVersionedQuery(GET_LIVE_TRAFFIC_LEARNING, kit.PlatformType.K8s, version);
    assert.match(selected, /workloads\(filter: \{ namespace: \$namespace, kind: \$kind, name: \$name \}\)/, version);
    assert.match(selected, /liveTrafficLearning \{/, version);
  }
  for (const platformType of [kit.PlatformType.Vm, kit.PlatformType.Connector]) {
    assert.equal(getVersionedQuery(GET_LIVE_TRAFFIC_LEARNING, platformType, 'v1.40.0'), undefined, platformType);
  }
});
