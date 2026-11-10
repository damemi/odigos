import { useCallback, useEffect, useRef } from 'react';
import { PlatformType } from '@odigos/ui-kit/types';
import { useDrawerStore } from '@odigos/ui-kit/store';
import { useApiLazyQuery } from '@odigos/ui-kit/contexts';
import { shouldPollVmSources, startVmSourcesPoller } from './vmSourcesPolling';

// Same variables the kit's `fetchAll` sends, so the result lands in the same Apollo cache entry
// and the sources list updates.
const WORKLOADS_VARS = { filter: { markedForInstrumentation: true } };

// Keeps the sources list of a VM agent fresh when the agent doesn't send source events
// (older agents). Returns `markAgentSendsEvents`, to be called on a source event from
// the current proxy; polling then stops for that proxy. Silent: `sourcesApi.fetchAll` raises an
// error toast on failure, so this uses the lazy query, which reports errors only to the caller.
export const useVmSourcesPolling = (proxyID?: string, platformType?: PlatformType) => {
  const { execute } = useApiLazyQuery('GET_WORKLOADS', { fetchPolicy: 'network-only' });
  const drawerOpen = useDrawerStore((s) => s.drawerType !== null);

  const proxiesSendingEvents = useRef(new Set<string>());
  const markAgentSendsEvents = useCallback(() => {
    if (proxyID) proxiesSendingEvents.current.add(proxyID);
  }, [proxyID]);

  const executeRef = useRef(execute);
  const drawerOpenRef = useRef(drawerOpen);
  useEffect(() => {
    executeRef.current = execute;
    drawerOpenRef.current = drawerOpen;
  }, [execute, drawerOpen]);

  const isVmPlatform = platformType === PlatformType.Vm;

  useEffect(() => {
    if (!proxyID || !isVmPlatform) return;

    return startVmSourcesPoller({
      canPoll: () =>
        shouldPollVmSources({
          isVmPlatform,
          agentSendsEvents: proxiesSendingEvents.current.has(proxyID),
          tabHidden: document.hidden,
          drawerOpen: drawerOpenRef.current,
        }),
      fetch: async () => !(await executeRef.current(WORKLOADS_VARS)).error,
    });
  }, [proxyID, isVmPlatform]);

  return { markAgentSendsEvents };
};
