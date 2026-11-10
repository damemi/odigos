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
const deps = { '@odigos/ui-kit/types': kit };
const profiling = load('../src/graphql/mutations/remote/profiling.ts', deps);
const agents = load('../src/graphql/queries/remote/instrumentation-agents.ts', deps);
const goOffsets = load('../src/graphql/mutations/remote/go-offsets.ts', deps);

// The first odigos release whose schema accepts each document.
const cases = [
  ['DISABLE_SOURCE_PROFILING', profiling.DISABLE_SOURCE_PROFILING, 'v1.24.3', 'v1.25.0', /disableSourceProfiling\(/],
  ['CONFIGURE_PROFILING_CACHE', profiling.CONFIGURE_PROFILING_CACHE, 'v1.32.4', 'v1.33.0', /configureProfilingCache\(slotTTLSeconds/],
  ['GET_INSTRUMENTATION_AGENTS', agents.GET_INSTRUMENTATION_AGENTS, 'v1.37.2', 'v1.38.0', /instrumentationAgents \{/],
  ['GET_GO_OFFSETS', agents.GET_GO_OFFSETS, 'v1.37.2', 'v1.38.0', /goOffsets \{/],
  ['CHECK_GO_OFFSETS_UPDATES', goOffsets.CHECK_GO_OFFSETS_UPDATES, 'v1.37.2', 'v1.38.0', /checkGoOffsetsUpdates\(content/],
  ['UPDATE_GO_OFFSETS', goOffsets.UPDATE_GO_OFFSETS, 'v1.37.2', 'v1.38.0', /updateGoOffsets\(content/],
];

for (const [name, document, before, from, shape] of cases) {
  test(`${name} resolves only on Kubernetes proxies from ${from}`, () => {
    assert.equal(getVersionedQuery(document, kit.PlatformType.K8s, before), undefined, before);
    for (const version of [from, 'v1.40.0']) assert.match(getVersionedQuery(document, kit.PlatformType.K8s, version), shape, version);
    for (const platformType of [kit.PlatformType.Vm, kit.PlatformType.Connector]) assert.equal(getVersionedQuery(document, platformType, 'v1.40.0'), undefined, platformType);
  });
}
