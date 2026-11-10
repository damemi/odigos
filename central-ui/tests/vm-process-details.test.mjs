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

// The kit's runtime enums, with the real string values (its bundle needs a browser to load).
const ProgrammingLanguages = {
  Go: 'go',
  JavaScript: 'javascript',
  Python: 'python',
  Java: 'java',
  DotNet: 'dotnet',
  CSharp: 'csharp',
  CPlusPlus: 'cplusplus',
  Php: 'php',
  Ruby: 'ruby',
  Rust: 'rust',
  Swift: 'swift',
  Elixir: 'elixir',
  Unknown: 'unknown',
};
const DesiredStateProgress = { Success: 'Success' };

const utils = load('../src/graphql/remote/utils.ts');
const dialect = { dialectVersion: (ctx) => ctx.schemaVersion ?? ctx.version };
const reports = load('../src/graphql/remote/vm-workload-reports.ts', {
  '@/graphql/remote/utils': utils,
  '@/graphql/remote/vm-dialect': dialect,
  '@odigos/ui-kit/types': { DesiredStateProgress, ProgrammingLanguages },
});
const sources = load('../src/graphql/remote/vm-sources.ts', {
  '@/graphql/remote/utils': utils,
  '@/graphql/remote/vm-dialect': dialect,
  '@/graphql/remote/vm-workload-reports': reports,
});

const runtime = (language, version) => ({ detected: { language, version }, override: null, running: { language, version } });

// A systemd unit that runs a Java service next to a C++ helper, plus a process the
// instrumentation controller excluded for its language (metadata only, no instance row).
const mixedUnit = {
  serviceName: 'billing-engine.service',
  kind: 'systemd',
  region: null,
  desired: { enabled: false },
  instances: [
    {
      id: 'billing-41388',
      pid: 41388,
      alive: true,
      runtimeName: 'java',
      runtimeVersion: '21.0.2',
      instrumented: true,
      probesAttached: true,
      message: 'attached',
      distro: 'java-ebpf-instrumentations',
    },
    { id: 'billing-41390', pid: 41390, alive: true, instrumented: false, probesAttached: false },
  ],
  processMetadata: [
    {
      pid: 41388,
      exe: '/usr/bin/java',
      cmdline: 'java -jar /opt/billing/billing.jar --port 8080',
      runtime: runtime('java', '21.0.2'),
      instrumentationEligible: true,
      instrumented: true,
      probesAttached: true,
      excludedReason: null,
    },
    {
      pid: 41390,
      exe: '/opt/billing/bin/ledger-sync',
      cmdline: '/opt/billing/bin/ledger-sync --interval 30s',
      runtime: runtime('cplusplus', null),
      instrumentationEligible: false,
      instrumented: false,
      probesAttached: null,
      excludedReason: 'process language does not match workload instrumentation target',
    },
    {
      pid: 41400,
      exe: '/usr/bin/python3',
      cmdline: 'python3 /opt/billing/report.py',
      runtime: runtime('python', '3.12.1'),
      instrumentationEligible: false,
      instrumented: false,
      excludedReason: 'pid not in source include_pids',
    },
  ],
};

test('a mixed-language unit maps PID, command line, language, version and eligibility per process', () => {
  const processes = reports.mapReportProcesses(mixedUnit);
  assert.equal(processes.length, 3);

  const [java, cpp, python] = processes;
  assert.deepEqual(
    {
      pid: java.pid,
      exe: java.exe,
      cmdline: java.cmdline,
      language: java.language,
      languageVersion: java.languageVersion,
      eligible: java.instrumentationEligible,
      reason: java.excludedReason,
      instrumented: java.instrumented,
    },
    { pid: 41388, exe: '/usr/bin/java', cmdline: 'java -jar /opt/billing/billing.jar --port 8080', language: 'java', languageVersion: '21.0.2', eligible: true, reason: null, instrumented: true },
  );
  assert.equal(java.distro, 'java-ebpf-instrumentations');

  assert.equal(cpp.language, 'cplusplus');
  assert.equal(cpp.languageVersion, null);
  assert.equal(cpp.instrumentationEligible, false);
  assert.equal(cpp.excludedReason, 'process language does not match workload instrumentation target');
  assert.equal(cpp.alive, true);

  // Metadata without an instance row still lists, as alive, with the agent's exclusion reason.
  assert.equal(python.pid, 41400);
  assert.equal(python.language, 'python');
  assert.equal(python.languageVersion, '3.12.1');
  assert.equal(python.excludedReason, 'pid not in source include_pids');
  assert.equal(python.message, null);
});

test('Add Sources workloads carry the same processes as the source drawer', () => {
  const { namespaces } = sources.mapVmWorkloadReportsToNamespaces({ workloadReports: [mixedUnit] });
  assert.equal(namespaces.length, 1);
  assert.equal(namespaces[0].name, 'systemd');

  const [workload] = namespaces[0].workloads;
  assert.deepEqual(workload.id, { namespace: 'systemd', name: 'billing-engine.service', kind: 'systemd' });
  assert.deepEqual(workload.processes, reports.mapReportProcesses(mixedUnit));
  assert.deepEqual(
    workload.processes.map((p) => p.language),
    ['java', 'cplusplus', 'python'],
  );

  const [drawer] = reports.mergeVmWorkloadReports(
    [{ id: { namespace: 'default', name: 'billing-engine.service', kind: 'systemd' }, markedForInstrumentation: { markedForInstrumentation: true } }],
    [mixedUnit],
  );
  assert.deepEqual(drawer.instrumentationReport.processes, workload.processes);
});

test('a field an older agent does not send stays unset instead of being filled in', () => {
  // Older agents report no `runtime` (or an empty one), and no eligibility fields.
  const legacy = {
    serviceName: 'worker',
    kind: 'process',
    instances: [{ id: 'worker-7', pid: 7, alive: true, instrumented: false, probesAttached: false }],
    processMetadata: [{ pid: 7, cmdline: 'worker --queue jobs', instrumented: false }],
  };
  const [process] = reports.mapReportProcesses(legacy);
  assert.equal(process.pid, 7);
  assert.equal(process.cmdline, 'worker --queue jobs');
  assert.equal(process.exe, null);
  assert.equal(process.language, null);
  assert.equal(process.languageVersion, null);
  assert.equal(process.instrumentationEligible, null);
  assert.equal(process.excludedReason, null);

  const emptyRuntime = reports.mapReportProcesses({
    serviceName: 'worker',
    kind: 'process',
    processMetadata: [{ pid: 8, runtime: { detected: { language: '', version: null } }, instrumentationEligible: true, instrumented: false }],
  });
  assert.equal(emptyRuntime[0].language, null);
  assert.equal(emptyRuntime[0].languageVersion, null);
  assert.equal(emptyRuntime[0].instrumentationEligible, true);
});

test('the instance row supplies the runtime when processMetadata has none', () => {
  const [process] = reports.mapReportProcesses({
    serviceName: 'api',
    kind: 'process',
    instances: [{ id: 'api-9', pid: 9, alive: true, runtimeName: 'golang', runtimeVersion: '1.23.4', instrumented: false, probesAttached: false }],
    processMetadata: [{ pid: 9, instrumentationEligible: true, instrumented: false }],
  });
  assert.equal(process.language, 'go');
  assert.equal(process.languageVersion, '1.23.4');
});

test('language names the kit has no value for map to unknown, not to a guess', () => {
  const [process] = reports.mapReportProcesses({
    serviceName: 'legacy',
    kind: 'process',
    processMetadata: [{ pid: 3, runtime: runtime('cobol', '85'), instrumentationEligible: false, instrumented: false }],
  });
  assert.equal(process.language, 'unknown');
  assert.equal(process.languageVersion, '85');
  for (const [reported, expected] of [
    ['C++', 'cplusplus'],
    ['cpp', 'cplusplus'],
    ['Node', 'javascript'],
    ['  Java ', 'java'],
  ]) {
    const [row] = reports.mapReportProcesses({
      serviceName: 's',
      kind: 'process',
      processMetadata: [{ pid: 1, runtime: runtime(reported, null), instrumentationEligible: true, instrumented: false }],
    });
    assert.equal(row.language, expected, reported);
  }
});

test('a report without processes yields no processes anywhere', () => {
  assert.equal(reports.mapReportProcesses({ serviceName: 'idle', kind: 'systemd', instances: [], processMetadata: [] }), null);
  assert.equal(reports.mapReportProcesses({ serviceName: 'idle', kind: 'systemd' }), null);

  const { namespaces } = sources.mapVmWorkloadReportsToNamespaces({
    workloadReports: [
      { serviceName: 'idle', kind: 'systemd', instances: [], processMetadata: [] },
      { serviceName: 'old-agent', kind: 'systemd' },
    ],
  });
  for (const workload of namespaces[0].workloads) assert.equal('processes' in workload, false, workload.id.name);
  assert.deepEqual(
    namespaces[0].workloads.map((w) => w.id.name),
    ['idle', 'old-agent'],
  );

  assert.deepEqual(sources.mapVmWorkloadReportsToNamespaces({ workloadReports: [] }), { namespaces: [] });
  assert.deepEqual(sources.mapVmWorkloadReportsToNamespaces(null), { namespaces: [] });
});
