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
const ProgrammingLanguages = { Java: 'java', CPlusPlus: 'cplusplus', Unknown: 'unknown' };
const DesiredStateProgress = { Success: 'Success', Waiting: 'Waiting', Failure: 'Failure', Unsupported: 'Unsupported', Unknown: 'Unknown', Pending: 'Pending', Disabled: 'Disabled' };

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

// Fixtures from the agent itself, at odigos-io/vm-agent@b32c45f4e6b3d82d78d65c413d6149b6ddb5b92e.
// Inputs a–c are the ones `pkg/components/domain/source/match_cmdline_test.go` round-trips
// (`TestPIDSourceRegionRoundTrip`, `TestSourceFromCreateInputPIDSourceViaRegion`); d adds
// padding-free URL-safe base64 and non-ASCII bytes. Every `region` is what the agent's
// `EncodePIDSourceRegion` (`pkg/components/domain/source/include_pids.go`) returned for it
// when run at that commit.
const AGENT_FIXTURES = [
  { pids: [10, 20], cmdline: 'cpp-worker alpha 18081', region: 'odigos.pid_source=v1;pids=10,20;cmdline=Y3BwLXdvcmtlciBhbHBoYSAxODA4MQ' },
  { pids: [3216510], cmdline: 'cpp-worker echo 1', region: 'odigos.pid_source=v1;pids=3216510;cmdline=Y3BwLXdvcmtlciBlY2hvIDE' },
  { pids: [7], cmdline: '', region: 'odigos.pid_source=v1;pids=7' },
  {
    pids: [41388, 41390],
    cmdline: '  /usr/bin/java -Dsvc=billing-ä -jar /opt/billing/app.jar?x=1  ',
    region: 'odigos.pid_source=v1;pids=41388,41390;cmdline=L3Vzci9iaW4vamF2YSAtRHN2Yz1iaWxsaW5nLcOkIC1qYXIgL29wdC9iaWxsaW5nL2FwcC5qYXI_eD0x',
  },
];

test('the PID-source region is byte for byte what the agent encodes, and decodes back', () => {
  for (const { pids, cmdline, region } of AGENT_FIXTURES) {
    assert.equal(utils.encodePidSourceRegion(pids, cmdline), region);
    // The agent trims the command line before encoding, so that is what comes back.
    assert.deepEqual(utils.decodePidSourceRegion(region), { pids, cmdline: cmdline.trim() });
    assert.equal(utils.isPidSourceRegion(region), true);
  }
  assert.equal(utils.encodePidSourceRegion([], '  '), '');
});

test('only PID-source encodings decode; provider regions are left alone', () => {
  // Older agents wrote only the PID list.
  assert.deepEqual(utils.decodePidSourceRegion('odigos.include_pids=7'), { pids: [7], cmdline: '' });
  for (const region of ['us-east-1', 'europe-west1', '', null, undefined, 'odigos.pid_source=v2;pids=1', 'odigos.pid_source=v1;cmdline=%%%']) {
    assert.equal(utils.isPidSourceRegion(region), false, String(region));
  }
});

// The agent's `CmdlineMatchPercent` (vm-agent@c7835cc5da0618a272fd29b708f42ac9fc168e8d
// `pkg/components/domain/source/match_cmdline.go`), ported to show why each process
// needs its own fingerprint: the agent drops a PID that scores below its threshold
// (85% by default) against its source's fingerprint.
const argvMatchedInOrder = (cur, saved) => {
  let matched = 0;
  let savedPos = 0;
  for (let i = 0; i < cur.length && savedPos < saved.length; i++) {
    const j = saved.indexOf(cur[i], savedPos);
    if (j >= 0) {
      matched++;
      savedPos = j + 1;
    } else savedPos++;
  }
  return matched;
};
const cmdlineMatchPercent = (saved, current) => {
  const savedArgs = saved.trim().split(/\s+/).filter(Boolean);
  const curArgs = current.trim().split(/\s+/).filter(Boolean);
  if (!curArgs.length || !savedArgs.length) return 0;
  const base = (p) => p.slice(p.lastIndexOf('/') + 1);
  if (base(savedArgs[0]) !== base(curArgs[0])) return 0;
  if (curArgs.length === 1) return 100;
  const pct = Math.floor((argvMatchedInOrder(curArgs.slice(1), savedArgs.slice(1)) * 100) / (curArgs.length - 1));
  return pct === 0 ? 1 : pct;
};

// The `mixed-workers` systemd unit on the proof VM: 4 C++ and 4 Java workers, with
// the PIDs, executables and command lines read from its /proc.
const CPP_EXE = '/opt/fixtures/mixed/cpp-worker';
const JAVA_EXE = '/usr/lib/jvm/java-17-openjdk-amd64/bin/java';
const cppWorkers = [9530, 9531, 9532, 9533].map((pid, i) => ({ pid, exe: CPP_EXE, cmdline: `${CPP_EXE} ${i + 1}`, language: 'cplusplus' }));
const javaWorkers = [9534, 9535, 9536, 9537].map((pid, i) => ({ pid, exe: JAVA_EXE, cmdline: `java -Xms32m -Xmx96m -cp /opt/fixtures/mixed Worker ${19101 + i}`, language: 'java' }));
const mixedWorkersReport = {
  serviceName: 'mixed-workers.service',
  kind: 'systemd',
  region: null,
  desired: { enabled: false },
  instances: [],
  processMetadata: [...cppWorkers, ...javaWorkers].map(({ pid, exe, cmdline, language }) => ({
    pid,
    exe,
    cmdline,
    runtime: { detected: { language, version: null }, override: null, running: null },
    instrumentationEligible: true,
    instrumented: false,
  })),
};

// What ui-kit hands `PERSIST_SOURCES` for a pick of processes: one entry per workload and
// language, with the first picked process's command line only.
const pickEntry = (workers, language) => ({
  namespace: 'systemd',
  name: 'mixed-workers.service',
  kind: 'systemd',
  selected: true,
  currentStreamName: 'default',
  pids: workers.map((w) => w.pid),
  cmdline: workers[0].cmdline,
  language,
});

const discover = (...reportList) => {
  utils.clearVmProcesses();
  sources.mapVmWorkloadReportsToNamespaces({ workloadReports: reportList });
};

test('Add Sources marks a workload without a source as no source, not as a disabled one', () => {
  // The kit reads `false` as an explicitly disabled Source: it badges the row Disabled and makes no PID sources from it.
  const marked = (desired) =>
    sources.mapVmWorkloadReportsToNamespaces({ workloadReports: [{ ...mixedWorkersReport, desired }] }).namespaces[0].workloads[0].markedForInstrumentation.markedForInstrumentation;
  assert.equal(marked({ enabled: false, sourceMatched: false, sourceEnabled: false }), null);
  assert.equal(marked({ enabled: false }), null);
  assert.equal(marked({ enabled: false, sourceMatched: true, sourceEnabled: false }), false);
  assert.equal(marked({ enabled: true, sourceMatched: true, sourceEnabled: true }), true);
});

test("one fingerprint per group would leave 3 of each fixture group's 4 workers out", () => {
  for (const group of [cppWorkers, javaWorkers]) {
    const [first, ...siblings] = group;
    for (const sibling of siblings) assert.ok(cmdlineMatchPercent(first.cmdline, sibling.cmdline) < 85, sibling.cmdline);
  }
});

test('a picked C++ group becomes one process source per worker, as odictl makes them', () => {
  discover(mixedWorkersReport);
  const { query, variables } = sources.buildVmPersistSources({ sources: [pickEntry(cppWorkers, 'cplusplus')] });

  assert.equal((query.match(/createSource\(/g) ?? []).length, 4);
  // odictl suggests `cpp-worker` for all four (the only trailing token is a number), so each takes its PID.
  assert.deepEqual(Object.values(variables), [
    ...cppWorkers.map((w) => ({
      name: `cpp-worker-${w.pid}`,
      kind: 'process',
      enabled: true,
      region: utils.encodePidSourceRegion([w.pid], w.cmdline),
      config: { runtimeOverride: { language: 'cplusplus' } },
    })),
  ]);

  // Each source holds exactly its own PID, and its fingerprint scores 100% against its own process.
  Object.values(variables).forEach((input, i) => {
    const { pids, cmdline } = utils.decodePidSourceRegion(input.region);
    assert.deepEqual(pids, [cppWorkers[i].pid]);
    assert.equal(cmdlineMatchPercent(cmdline, cppWorkers[i].cmdline), 100);
  });
});

test('picking both groups of the mixed unit makes 8 sources, one per PID, all distinct', () => {
  discover(mixedWorkersReport);
  const { variables } = sources.buildVmPersistSources({
    sources: [{ namespace: 'systemd', name: 'nginx.service', kind: 'systemd', selected: true, currentStreamName: 'default' }, pickEntry(cppWorkers, 'cplusplus'), pickEntry(javaWorkers, 'java')],
  });
  const inputs = Object.values(variables);

  // A whole workload keeps today's payload.
  assert.deepEqual(inputs[0], { name: 'nginx.service', kind: 'systemd', enabled: true });

  const pidSources = inputs.slice(1);
  assert.deepEqual(
    pidSources.map((input) => input.name),
    ['cpp-worker-9530', 'cpp-worker-9531', 'cpp-worker-9532', 'cpp-worker-9533', 'Worker-9534', 'Worker-9535', 'Worker-9536', 'Worker-9537'],
  );
  assert.equal(new Set(pidSources.map((input) => `${input.name}\0${input.kind}`)).size, 8);
  pidSources.forEach((input, i) => {
    const worker = [...cppWorkers, ...javaWorkers][i];
    const { pids, cmdline } = utils.decodePidSourceRegion(input.region);
    assert.deepEqual(pids, [worker.pid]);
    assert.equal(cmdlineMatchPercent(cmdline, worker.cmdline), 100);
    assert.equal(input.config.runtimeOverride.language, worker.language);
  });
});

test("a process odictl names uniquely gets odictl's exact name", () => {
  // The process from the agent's own `TestSourceFromCreateInputPIDSourceViaRegion`.
  discover({
    serviceName: 'farm.service',
    kind: 'systemd',
    processMetadata: [{ pid: 3216510, exe: '/opt/farm/cpp-worker', cmdline: 'cpp-worker echo 1', runtime: { detected: { language: 'cplusplus' } }, instrumented: false }],
  });
  const { variables } = sources.buildVmPersistSources({
    sources: [{ namespace: 'systemd', name: 'farm.service', kind: 'systemd', selected: true, currentStreamName: 'default', pids: [3216510], cmdline: 'cpp-worker echo 1', language: 'cplusplus' }],
  });
  assert.deepEqual(variables.input0, { name: 'echo', kind: 'process', enabled: true, region: AGENT_FIXTURES[1].region, config: { runtimeOverride: { language: 'cplusplus' } } });
});

test('a new PID source never takes the name of a configured one', () => {
  discover({
    serviceName: 'farm.service',
    kind: 'systemd',
    processMetadata: [{ pid: 3216511, exe: '/opt/farm/cpp-worker', cmdline: 'cpp-worker echo 2', runtime: { detected: { language: 'cplusplus' } }, instrumented: false }],
  });
  // `echo` already exists, e.g. made by odictl; a create under that name would replace it.
  sources.mapVmSourcesToWorkloads({ sources: [source('echo', 'process', AGENT_FIXTURES[1].region)] });
  const { variables } = sources.buildVmPersistSources({
    sources: [{ namespace: 'systemd', name: 'farm.service', kind: 'systemd', selected: true, currentStreamName: 'default', pids: [3216511], cmdline: 'cpp-worker echo 2', language: 'cplusplus' }],
  });
  assert.equal(variables.input0.name, 'echo-3216511');
});

test('a PID with no report behind it still gets its own source', () => {
  utils.clearVmProcesses();
  const { variables } = sources.buildVmPersistSources({
    sources: [{ namespace: 'process', name: 'worker', kind: 'process', selected: true, currentStreamName: 'default', pids: [77, 78], cmdline: 'worker --queue jobs' }],
  });
  // The kit's command line is the first PID's; the second has nothing to name or find it by but its PID.
  assert.deepEqual(variables.input0, { name: 'jobs', kind: 'process', enabled: true, region: 'odigos.pid_source=v1;pids=77;cmdline=d29ya2VyIC0tcXVldWUgam9icw' });
  assert.deepEqual(variables.input1, { name: 'pid-78', kind: 'process', enabled: true, region: 'odigos.pid_source=v1;pids=78' });
});

test("odictl's suggested name, token by token", () => {
  assert.equal(utils.suggestVmProcessName(1, '/opt/fixtures/mixed/cpp-worker 1', '/opt/fixtures/mixed/cpp-worker'), 'cpp-worker');
  assert.equal(utils.suggestVmProcessName(1, 'java -Xms32m -cp /opt/x Worker 19101', '/usr/bin/java'), 'Worker');
  assert.equal(utils.suggestVmProcessName(1, 'cpp-worker alpha 18081', null), 'alpha');
  assert.equal(utils.suggestVmProcessName(1, '', '/usr/sbin/nginx'), 'nginx');
  assert.equal(utils.suggestVmProcessName(42, '/bin/run --flag', null), 'pid-42');
  // Go's Atoi rejects a number past int64, so odictl keeps it as a label.
  assert.equal(utils.suggestVmProcessName(1, 'svc 99999999999999999999', null), '99999999999999999999');
});

const source = (name, kind, region) => ({ name, kind, serviceName: null, enabled: true, region, config: null });
const report = (serviceName, kind, pids, state) => ({
  serviceName,
  kind,
  region: null,
  desired: { enabled: true, sourceEnabled: true },
  current: { state, instrumented: state === 'instrumented' },
  progress: { stage: state },
  instances: pids.map((pid) => ({ id: `${serviceName}-${pid}`, pid, alive: true, instrumented: state === 'instrumented', probesAttached: false })),
  processMetadata: [],
});

// Un-picking: deletes target the configured source that holds the PID, by its real name.
const unpick = (pids) => ({ namespace: 'systemd', name: 'mixed-workers.service', kind: 'systemd', selected: false, currentStreamName: 'default', pids });
const knownSources = (...list) => {
  utils.clearVmProcesses();
  sources.mapVmSourcesToWorkloads({ sources: list });
};
const regionFor = (w) => utils.encodePidSourceRegion([w.pid], w.cmdline);

test('un-picking a PID source created without a suffix deletes it by that name', () => {
  // `echo` was created as odictl names it; re-deriving a name now would give `echo-3216510`.
  knownSources(source('echo', 'process', AGENT_FIXTURES[1].region), source('go-orders', 'systemd', null));
  const { query, variables } = sources.buildVmPersistSources({ sources: [{ ...unpick([3216510]), name: 'farm.service' }] });
  assert.match(query, /s0: deleteSource\(input: \$input0\)/);
  assert.equal((query.match(/Source\(/g) ?? []).length, 1);
  assert.deepEqual(variables.input0, { name: 'echo', kind: 'process', region: AGENT_FIXTURES[1].region });
});

test('un-picking a PID source created with a suffix deletes only that one', () => {
  const [cpp1, cpp2] = cppWorkers;
  knownSources(source('cpp-worker-9530', 'process', regionFor(cpp1)), source('cpp-worker-9531', 'process', regionFor(cpp2)));
  const { query, variables } = sources.buildVmPersistSources({ sources: [unpick([9531])] });
  assert.equal((query.match(/deleteSource\(/g) ?? []).length, 1);
  assert.deepEqual(variables.input0, { name: 'cpp-worker-9531', kind: 'process', region: regionFor(cpp2) });
});

test('un-picking a PID no known source holds deletes nothing', () => {
  knownSources(source('cpp-worker-9530', 'process', regionFor(cppWorkers[0])), source('go-orders', 'systemd', null));
  assert.deepEqual(sources.buildVmPersistSources({ sources: [unpick([9599])] }), { query: 'mutation PersistVmSources { __typename }', variables: {} });
});

test("un-checking a PID source's own row in Add Sources deletes it by its name", () => {
  // What the kit sends today: the agent lists a PID source's processes as a workload named for the source.
  knownSources(source('cpp-worker-9531', 'process', regionFor(cppWorkers[1])));
  const { variables } = sources.buildVmPersistSources({
    sources: [{ namespace: 'process', name: 'cpp-worker-9531', kind: 'process', selected: false, currentStreamName: 'default' }],
  });
  assert.deepEqual(variables.input0, { name: 'cpp-worker-9531', kind: 'process' });
});

test('PID sources from Central and from odictl each get their own report', () => {
  const [cpp1, cpp2] = cppWorkers;
  const { workloads } = sources.mapVmSourcesWithReportsToWorkloads({
    sources: [
      source('cpp-worker-9530', 'process', regionFor(cpp1)),
      source('cpp-worker-9531', 'process', regionFor(cpp2)),
      // Made by `odictl`, named for the process.
      source('echo', 'process', AGENT_FIXTURES[1].region),
      source('nginx.service', 'systemd', null),
    ],
    // The agent reports a PID source under the source's own name+kind, with no region.
    workloadReports: [
      report('cpp-worker-9530', 'process', [9530], 'instrumented'),
      report('cpp-worker-9531', 'process', [9531], 'attaching'),
      report('echo', 'process', [3216510], 'instrumented'),
      report('nginx.service', 'systemd', [900], 'not_instrumented'),
    ],
  });

  assert.equal(workloads.length, 4);
  const [first, second, echo, nginx] = workloads;

  // Two PID sources of one unit stay two rows, each with its own status and process.
  assert.equal(first.id.region, regionFor(cpp1));
  assert.equal(second.id.region, regionFor(cpp2));
  assert.equal(first.workloadOdigosHealthStatus.status, 'Success');
  assert.deepEqual(
    first.instrumentationReport.processes.map((p) => p.pid),
    [9530],
  );
  assert.equal(second.workloadOdigosHealthStatus.status, 'Waiting');
  assert.deepEqual(
    second.instrumentationReport.processes.map((p) => p.pid),
    [9531],
  );

  assert.equal(echo.workloadOdigosHealthStatus.status, 'Success');
  assert.deepEqual(
    echo.instrumentationReport.processes.map((p) => p.pid),
    [3216510],
  );

  // Whole workloads merge exactly as before.
  assert.equal(nginx.workloadOdigosHealthStatus.status, 'Pending');
});

test('a provider region still has to match the report region', () => {
  const merged = reports.mergeVmWorkloadReports(
    [{ id: { namespace: 'default', name: 'checkout', kind: 'gcp.cloud-run', region: 'us-east1' }, markedForInstrumentation: { markedForInstrumentation: true } }],
    [{ ...report('checkout', 'gcp.cloud-run', [], 'instrumented'), region: 'europe-west1' }],
  );
  assert.equal(merged[0].instrumentationReport, undefined);
});
