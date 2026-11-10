import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test, mock } from 'node:test';
import { createRequire } from 'node:module';
import ts from 'typescript';

const require = createRequire(import.meta.url);
const load = (file, dependencies = {}) => {
  const js = ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
  const mod = { exports: {} };
  new Function('require', 'module', 'exports', js)((id) => dependencies[id] ?? require(id), mod, mod.exports);
  return mod.exports;
};
const sseEvents = load('../src/hooks/common/sseEvents.ts');
const { shouldPollVmSources, startVmSourcesPoller, nextPollDelay, shouldToastSseEvent, shouldRefetchAllSources, VM_SOURCES_POLL_INTERVAL_MS, VM_SOURCES_POLL_MAX_BACKOFF_MS } = load('../src/hooks/common/vmSourcesPolling.ts', { './sseEvents': sseEvents });

const base = { isVmPlatform: true, agentSendsEvents: false, tabHidden: false, drawerOpen: false };
const tick = async (ms) => {
  mock.timers.tick(ms);
  for (let i = 0; i < 5; i++) await Promise.resolve();
};

test('polls about every 10s', () => assert.equal(VM_SOURCES_POLL_INTERVAL_MS, 10000));
test('polls a VM agent that has not sent events', () => assert.equal(shouldPollVmSources(base), true));
test('stops once the agent sends events', () => assert.equal(shouldPollVmSources({ ...base, agentSendsEvents: true }), false));
test('pauses while a drawer or form is open', () => assert.equal(shouldPollVmSources({ ...base, drawerOpen: true }), false));
test('pauses while the tab is hidden', () => assert.equal(shouldPollVmSources({ ...base, tabHidden: true }), false));
test('does nothing on non-VM platforms', () => assert.equal(shouldPollVmSources({ ...base, isVmPlatform: false }), false));

test('backs off on failures, capped, and resets on success', () => {
  assert.equal(nextPollDelay(0), 10000);
  assert.equal(nextPollDelay(1), 20000);
  assert.equal(nextPollDelay(2), 40000);
  assert.equal(nextPollDelay(20), VM_SOURCES_POLL_MAX_BACKOFF_MS);
});

test('poller fetches on each interval and stops on cleanup (unmount or proxy switch)', async () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    let calls = 0;
    const stop = startVmSourcesPoller({ canPoll: () => true, fetch: async () => (calls++, true) });
    await tick(10000);
    await tick(10000);
    assert.equal(calls, 2);
    stop();
    await tick(60000);
    assert.equal(calls, 2);
  } finally {
    mock.timers.reset();
  }
});

test('poller skips fetches while paused (hidden tab, open drawer, agent events) and resumes after', async () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    let calls = 0;
    let state = { ...base, tabHidden: true };
    const stop = startVmSourcesPoller({ canPoll: () => shouldPollVmSources(state), fetch: async () => (calls++, true) });
    await tick(10000);
    assert.equal(calls, 0);
    state = { ...base, drawerOpen: true };
    await tick(10000);
    assert.equal(calls, 0);
    state = base;
    await tick(10000);
    assert.equal(calls, 1);
    state = { ...base, agentSendsEvents: true };
    await tick(10000);
    assert.equal(calls, 1);
    stop();
  } finally {
    mock.timers.reset();
  }
});

test('failed or throwing fetches never reject and back off instead of retrying every 10s', async () => {
  mock.timers.enable({ apis: ['setTimeout'] });
  try {
    let calls = 0;
    const stop = startVmSourcesPoller({ canPoll: () => true, fetch: async () => (calls++, Promise.reject(new Error('agent offline'))) });
    await tick(10000);
    assert.equal(calls, 1);
    await tick(10000);
    assert.equal(calls, 1, 'next attempt waits 20s after one failure');
    await tick(10000);
    assert.equal(calls, 2);
    stop();
  } finally {
    mock.timers.reset();
  }
});

test('VM agent source event (PLAT-2049 payload) refetches all sources without a toast', () => {
  const event = JSON.parse('{"type":"success","event":"Added","data":"3 sources added","crdType":"InstrumentationConfig","targets":[]}');
  assert.equal(shouldToastSseEvent({ crdType: event.crdType, title: event.event, message: event.data }), false);
  assert.equal(shouldRefetchAllSources(event.targets), true);
  for (const name of ['Modified', 'Deleted']) assert.equal(shouldToastSseEvent({ crdType: event.crdType, title: name, message: event.data }), false);
});

test('other events still toast', () => {
  assert.equal(shouldToastSseEvent({ crdType: 'Other', title: 'Added', message: 'x' }), true);
  assert.equal(shouldToastSseEvent({ crdType: 'connected', title: '', message: 'x' }), false);
});
