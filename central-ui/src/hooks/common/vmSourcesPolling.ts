import { EventTypes, OpenSourceCrdTypes, OpenSourceEventTypes } from './sseEvents';

export const VM_SOURCES_POLL_INTERVAL_MS = 10000;
export const VM_SOURCES_POLL_MAX_BACKOFF_MS = 120000;

interface PollState {
  isVmPlatform: boolean;
  agentSendsEvents: boolean;
  tabHidden: boolean;
  drawerOpen: boolean;
}

// VM sources have no cluster informer, so the list is refreshed by agent
// `InstrumentationConfig` events. Agents that never send one are polled instead; the
// first event proves the agent emits them and stops polling for that proxy.
export const shouldPollVmSources = ({ isVmPlatform, agentSendsEvents, tabHidden, drawerOpen }: PollState): boolean => isVmPlatform && !agentSendsEvents && !tabHidden && !drawerOpen;

// Doubles the delay for each consecutive failure (an offline agent), capped, and resets on success.
export const nextPollDelay = (consecutiveFailures: number): number => Math.min(VM_SOURCES_POLL_INTERVAL_MS * 2 ** consecutiveFailures, VM_SOURCES_POLL_MAX_BACKOFF_MS);

interface PollerOptions {
  // Resolves to true on success. Must be silent: no notifications on failure.
  fetch: () => Promise<boolean>;
  canPoll: () => boolean;
}

// Self-scheduling loop (not an interval) so a slow or failing agent is never hit
// concurrently and failures back off. Returns a function that stops it.
export const startVmSourcesPoller = ({ fetch, canPoll }: PollerOptions): (() => void) => {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let failures = 0;
  let stopped = false;

  const schedule = () => {
    if (stopped) return;
    timer = setTimeout(async () => {
      if (canPoll()) {
        const ok = await fetch().catch(() => false);
        failures = ok ? 0 : failures + 1;
      }
      schedule();
    }, nextPollDelay(failures));
  };

  schedule();

  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
  };
};

interface SseNotice {
  crdType?: string;
  title?: string;
  message?: unknown;
}

// Source and destination events refresh data silently (the kit toasts creates and deletes itself),
// so only other events (e.g. cluster-level ones) raise a toast. VM agent events carry
// `crdType: InstrumentationConfig` and `type: 'success'` and must never toast.
export const shouldToastSseEvent = ({ crdType, title, message }: SseNotice): boolean =>
  !!(title || message) && crdType !== EventTypes.CONNECTED && title !== OpenSourceEventTypes.MODIFIED && crdType !== OpenSourceCrdTypes.InstrumentationConfig && crdType !== OpenSourceCrdTypes.Destination;

// Agent events carry no targets (always `[]`), which means "refetch every source".
export const shouldRefetchAllSources = (targets: string[]): boolean => targets.length === 0;
