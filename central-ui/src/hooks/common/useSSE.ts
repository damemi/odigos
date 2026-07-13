import { useEffect, useRef } from 'react';
import { useProxy } from '../proxy';
import { getSSEEventsUrl } from '@/utils';
import { StatusType } from '@odigos/ui-kit/types';
import { safeJsonParse } from '@odigos/ui-kit/functions';
import { useOdigosApi } from '@odigos/ui-kit/contexts';
import { type NotifyPayload, useNotificationStore, useProgressStore, ProgressKeys } from '@odigos/ui-kit/store';

enum EventTypes {
  CONNECTED = 'connected',
  K8S_CLUSTER_EVENT = 'k8s_cluster_event',
}

enum OpenSourceEventTypes {
  ADDED = 'Added',
  MODIFIED = 'Modified',
  DELETED = 'Deleted',
}

enum OpenSourceCrdTypes {
  InstrumentationConfig = 'InstrumentationConfig',
  Destination = 'Destination',
}

interface OpenSourceSSE {
  crdType: OpenSourceCrdTypes;
  event: OpenSourceEventTypes;
  data: string;
  target?: string; // @deprecated-legacy-api(v1.20) - Remove when min supported proxy >= v1.20
  targets?: string[];
  type: StatusType;
}

interface SseEvent {
  type: EventTypes;
  data: string | OpenSourceSSE;
  cluster_id?: string;
  cluster_name?: string;
  timestamp?: number;
}

interface DebouncedEvent {
  handler: NodeJS.Timeout | null;
  targets: string[];
}

const EVENT_DEBOUNCE_MS = 5000;

export const useSSE = () => {
  const { proxyID } = useProxy();
  const { addNotification } = useNotificationStore();
  const { sourcesApi, destinationsApi } = useOdigosApi();
  const fetchSources = sourcesApi.fetchAll;
  const fetchSourcesByTargets = sourcesApi.fetchByTargets;
  const fetchDestinations = destinationsApi.fetchAll;

  const maxRetries = 10;
  const retryCount = useRef(0);

  const eventsRef = useRef<Partial<Record<OpenSourceEventTypes, DebouncedEvent>> | null>(null);

  const resetEventHandler = (eventType: OpenSourceEventTypes) => {
    if (eventsRef.current?.[eventType]) {
      if (eventsRef.current[eventType]!.handler) {
        clearTimeout(eventsRef.current[eventType]!.handler as NodeJS.Timeout);
        eventsRef.current[eventType]!.handler = null;
      }
      eventsRef.current[eventType]!.targets = [];
    }
  };

  const handleEvent = (eventType: OpenSourceEventTypes, targets: string[], successCallback: (accumulatedTargets: string[]) => void) => {
    if (!eventsRef.current) {
      eventsRef.current = {};
    }
    if (!eventsRef.current[eventType]) {
      eventsRef.current[eventType] = { handler: null, targets: [] };
    }

    const entry = eventsRef.current[eventType]!;

    const existing = new Set(entry.targets);
    for (const t of targets) {
      if (t && !existing.has(t)) {
        entry.targets.push(t);
      }
    }

    if (entry.handler) clearTimeout(entry.handler as NodeJS.Timeout);

    entry.handler = setTimeout(() => {
      const accumulated = [...entry.targets];
      resetEventHandler(eventType);
      successCallback(accumulated);
    }, EVENT_DEBOUNCE_MS);
  };

  useEffect(() => {
    if (!proxyID) return;

    const connect = () => {
      const es = new EventSource(getSSEEventsUrl(proxyID));

      es.onopen = () => {
        retryCount.current = 0;
      };

      es.onerror = () => {
        es.close();

        if (retryCount.current < maxRetries) {
          retryCount.current += 1;
          console.warn(`Disconnected from the server. Retrying connection (${retryCount.current})`);

          setTimeout(() => connect(), Math.min(10000, 1000 * Math.pow(2, retryCount.current)));
        } else {
          console.error(`Connection lost on ${new Date().toLocaleString()}. Please reboot the application`);
        }
      };

      es.onmessage = (e: MessageEvent) => {
        const data = safeJsonParse<SseEvent | null>(e.data, null);
        const { type, data: sseData, cluster_id } = data || {};

        const isThisCluster = cluster_id && cluster_id === proxyID;
        const isK8sClusterEvent = type && type === EventTypes.K8S_CLUSTER_EVENT;

        if (isThisCluster && isK8sClusterEvent) {
          const targets: string[] =
            typeof sseData === 'object' && Array.isArray(sseData?.targets)
              ? sseData.targets
              : // @deprecated-legacy-api(v1.20) - Remove this fallback to single target
                typeof sseData === 'object' && sseData?.target
                ? [sseData.target]
                : [];

          const notification: NotifyPayload = {
            type: typeof sseData === 'object' ? sseData?.type : StatusType.Info,
            title: typeof sseData === 'object' ? sseData?.event || '' : '',
            message: typeof sseData === 'object' ? sseData?.data || '' : sseData,
            crdType: typeof sseData === 'object' ? sseData?.crdType || '' : '',
          };

          const isConnected = notification.crdType === EventTypes.CONNECTED;
          const isSource = notification.crdType === OpenSourceCrdTypes.InstrumentationConfig;
          const isDestination = notification.crdType === OpenSourceCrdTypes.Destination;

          // Destination create/delete success toasts are dispatched client-side
          // by the kit's `useDestinationsApi` (consistent with sources and every
          // other entity). Exclude them here so we don't double-notify; the
          // destination branch below still drives the refetch.
          if ((notification.title || notification.message) && !isConnected && notification.title !== OpenSourceEventTypes.MODIFIED && !isSource && !isDestination) {
            addNotification(notification);
          }

          if (isSource) {
            switch (notification.title) {
              case OpenSourceEventTypes.ADDED:
                const newCreated = Number(notification.message?.toString().replace(/[^\d]/g, '') || 0);
                useProgressStore.getState().addProgress(ProgressKeys.Instrumenting, newCreated);

                handleEvent(OpenSourceEventTypes.ADDED, targets, (accumulatedTargets) => {
                  const { resetProgress } = useProgressStore.getState();
                  resetProgress(ProgressKeys.Instrumenting);

                  if (accumulatedTargets.length > 0) {
                    fetchSourcesByTargets(accumulatedTargets, { slim: true });
                  } else {
                    fetchSources();
                  }
                });
                break;

              case OpenSourceEventTypes.MODIFIED:
                const { progress } = useProgressStore.getState();
                if (!progress[ProgressKeys.Instrumenting] && !progress[ProgressKeys.Uninstrumenting]) {
                  handleEvent(OpenSourceEventTypes.MODIFIED, targets, (accumulatedTargets) => {
                    if (accumulatedTargets.length > 0) {
                      fetchSourcesByTargets(accumulatedTargets, { slim: true });
                    } else {
                      fetchSources();
                    }
                  });
                }
                break;

              case OpenSourceEventTypes.DELETED:
                const newDeleted = Number(notification.message?.toString().replace(/[^\d]/g, '') || 0);
                useProgressStore.getState().addProgress(ProgressKeys.Uninstrumenting, newDeleted);

                handleEvent(OpenSourceEventTypes.DELETED, targets, () => {
                  const { resetProgress } = useProgressStore.getState();
                  resetProgress(ProgressKeys.Uninstrumenting);

                  // Re-fetch the canonical list — Apollo's cache write
                  // broadcasts to every active `useApiQuery('GET_WORKLOADS')`
                  // subscriber and the deleted workloads disappear from
                  // the response. The kit no longer exposes a per-id
                  // eviction primitive (`removeEntities`) — that lived
                  // on the deleted entity store.
                  fetchSources();
                });
                break;

              default:
                break;
            }
          } else if (isDestination) {
            handleEvent(OpenSourceEventTypes.MODIFIED, targets, () => {
              fetchDestinations();
            });
          } else {
            console.warn('Unhandled SSE for CRD type:', notification.crdType);
          }

          retryCount.current = 0;
        }
      };

      return es;
    };

    const es = connect();
    return () => {
      es?.close();
      Object.values(OpenSourceEventTypes).forEach((eventType) => resetEventHandler(eventType));
    };
  }, [proxyID]);

  return null;
};
