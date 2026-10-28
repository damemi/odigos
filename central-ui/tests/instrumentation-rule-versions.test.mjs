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
const queries = load('../src/graphql/queries/remote/instrumentation-rules.ts', { '@odigos/ui-kit/types': kit });
const mutations = load('../src/graphql/mutations/remote/instrumentation-rules.ts', { '@odigos/ui-kit/types': kit });

const documents = {
  GET_INSTRUMENTATION_RULES: queries.GET_INSTRUMENTATION_RULES,
  CREATE_INSTRUMENTATION_RULE: mutations.CREATE_INSTRUMENTATION_RULE,
  UPDATE_INSTURMENTATION_RULE: mutations.UPDATE_INSTURMENTATION_RULE,
};

for (const [name, document] of Object.entries(documents)) {
  test(`${name} selects kernelTraces only from v1.40`, () => {
    for (const version of ['v1.38.4', 'v1.39.0', 'v1.39.7']) {
      const selected = getVersionedQuery(document, kit.PlatformType.K8s, version);
      assert.ok(selected, version);
      assert.doesNotMatch(selected, /kernelTraces/, version);
    }
    for (const version of ['v1.40.0-pre1', 'v1.40.0', 'v1.41.2']) {
      const selected = getVersionedQuery(document, kit.PlatformType.K8s, version);
      assert.match(selected, /kernelTraces \{\s*filesystem \{\s*enabled\s*\}\s*subprocesses \{\s*recordCommandArgs\s*\}\s*cpuDelay \{\s*enabled\s*\}\s*\}/, version);
    }
  });
}
