import { useEffect, useRef } from 'react';
import { useProxy } from '../proxy';
import { useStatusStore } from '@/store';
import { getSSEEventsUrl } from '@/utils';
import { useSourceCRUD } from '../sources';
import { StatusType } from '@odigos/ui-kit/types';
import { useDestinationCRUD } from '../destinations';
import { NotificationIcon } from '@odigos/ui-kit/icons';
import { safeJsonParse } from '@odigos/ui-kit/functions';
import { NotifyPayload, useInstrumentStore, useNotificationStore } from '@odigos/ui-kit/store';

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
  target: string;
  type: StatusType;
}

interface SseEvent {
  type: EventTypes;
  data: string | OpenSourceSSE;
  cluster_id?: string;
  cluster_name?: string;
  timestamp?: number;
}

const EVENT_DEBOUNCE_MS = 5000;

export const useSSE = () => {
  const { proxyID } = useProxy();
  const { fetchSources } = useSourceCRUD();
  const { setStatusStore } = useStatusStore();
  const { addNotification } = useNotificationStore();
  const { fetchDestinations } = useDestinationCRUD();
  const { setInstrumentAwait, setInstrumentCount } = useInstrumentStore();

  const clearStatusMessage = () => {
    const { priorityMessage } = useStatusStore.getState();
    if (!priorityMessage) setStatusStore({ status: StatusType.Default, message: '', leftIcon: undefined });
  };

  const maxRetries = 10;
  const retryCount = useRef(0);

  const eventsRef = useRef<
    | {
        [eventType in OpenSourceEventTypes]?: {
          handler: NodeJS.Timeout | null;
          timestamp: number | null;
        };
      }
    | null
  >(null);

  const resetEventHandler = (eventType: OpenSourceEventTypes) => {
    if (eventsRef.current && eventsRef.current[eventType]) {
      if (eventsRef.current?.[eventType]?.handler) {
        clearTimeout(eventsRef.current[eventType]!.handler as NodeJS.Timeout);
        eventsRef.current[eventType]!.handler = null;
      }
      eventsRef.current[eventType]!.timestamp = null;
    }
  };

  const handleEvent = (eventType: OpenSourceEventTypes, successCallback: () => void) => {
    if (!eventsRef.current) {
      eventsRef.current = {
        [eventType]: {
          handler: null,
          timestamp: null,
        },
      };
    } else if (!eventsRef.current[eventType]) {
      eventsRef.current[eventType] = {
        handler: null,
        timestamp: null,
      };
    }

    if (eventsRef.current![eventType]!.handler) clearTimeout(eventsRef.current![eventType]!.handler as NodeJS.Timeout);
    eventsRef.current![eventType]!.timestamp = Date.now();

    // if last message was over `EVENT_DEBOUNCE_MS` time ago - fetch all sources.
    // once the condition is met, or a new event is received, the handler is cleared.
    eventsRef.current[eventType]!.handler = setTimeout(() => {
      resetEventHandler(eventType);
      successCallback();
    }, EVENT_DEBOUNCE_MS);
  };

  useEffect(() => {
    if (!proxyID) return;

    const connect = () => {
      const es = new EventSource(getSSEEventsUrl(proxyID));

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
        const isK8sClusterEvent = type && [EventTypes.K8S_CLUSTER_EVENT].includes(type);

        if (isThisCluster && isK8sClusterEvent) {
          const notification: NotifyPayload = {
            type: typeof sseData === 'object' ? sseData?.type : StatusType.Info,
            title: typeof sseData === 'object' ? sseData?.event || '' : '',
            message: typeof sseData === 'object' ? sseData?.data || '' : sseData,
            crdType: typeof sseData === 'object' ? sseData?.crdType || '' : '',
            target: typeof sseData === 'object' ? sseData?.target || '' : '',
          };

          const isSource = type === EventTypes.K8S_CLUSTER_EVENT && [OpenSourceCrdTypes.InstrumentationConfig].includes(notification.crdType);
          const isDestination = type === EventTypes.K8S_CLUSTER_EVENT && [OpenSourceCrdTypes.Destination].includes(notification.crdType);

          // do not notify for: connected, modified events, or sources
          if ((notification.title || notification.message) && notification.title !== OpenSourceEventTypes.MODIFIED && !isSource) {
            addNotification(notification);
          }

          // Handle specific CRD types
          if (isSource) {
            const { isAwaitingInstrumentation } = useInstrumentStore.getState();
            const { priorityMessage, message } = useStatusStore.getState();

            switch (notification.title) {
              case OpenSourceEventTypes.ADDED:
                if (!isAwaitingInstrumentation) setInstrumentAwait(true);

                const statusMessage1 = 'Creating sources, please wait a moment...';
                if (!priorityMessage && message !== statusMessage1) setStatusStore({ status: StatusType.Warning, message: statusMessage1, leftIcon: NotificationIcon });

                const { sourcesToCreate, sourcesCreated } = useInstrumentStore.getState();
                const totalCreated = sourcesCreated + Number(notification.message?.toString().replace(/[^\d]/g, '') || 0);
                if (sourcesToCreate < totalCreated) setInstrumentCount('sourcesToCreate', totalCreated + 1);
                setInstrumentCount('sourcesCreated', totalCreated);

                handleEvent(OpenSourceEventTypes.ADDED, () => {
                  const statusMessage2 = 'Instrumenting sources, please wait a moment...';
                  if (!priorityMessage && message !== statusMessage2) setStatusStore({ status: StatusType.Warning, message: statusMessage2, leftIcon: NotificationIcon });
                  addNotification({ type: StatusType.Success, title: OpenSourceEventTypes.ADDED, message: `Successfully created ${totalCreated} sources` });

                  fetchSources();

                  setInstrumentAwait(false);
                  setInstrumentCount('sourcesToCreate', 0);
                  setInstrumentCount('sourcesCreated', 0);
                });
                break;

              case OpenSourceEventTypes.MODIFIED:
                if (!isAwaitingInstrumentation) {
                  handleEvent(OpenSourceEventTypes.MODIFIED, () => {
                    clearStatusMessage();
                    fetchSources();
                  });
                }
                break;

              case OpenSourceEventTypes.DELETED:
                if (!isAwaitingInstrumentation) setInstrumentAwait(true);

                const { sourcesToDelete, sourcesDeleted } = useInstrumentStore.getState();
                const totalDeleted = sourcesDeleted + Number(notification.message?.toString().replace(/[^\d]/g, '') || 0);
                if (sourcesToDelete < totalDeleted) setInstrumentCount('sourcesToDelete', totalDeleted + 1);
                setInstrumentCount('sourcesDeleted', totalDeleted);

                handleEvent(OpenSourceEventTypes.DELETED, () => {
                  clearStatusMessage();
                  addNotification({ type: StatusType.Success, title: OpenSourceEventTypes.DELETED, message: `Successfully deleted ${totalDeleted} sources` });

                  fetchSources();

                  setInstrumentAwait(false);
                  setInstrumentCount('sourcesToDelete', 0);
                  setInstrumentCount('sourcesDeleted', 0);
                });
                break;

              default:
                break;
            }
          } else if (isDestination) {
            fetchDestinations();
          } else {
            console.warn('Unhandled SSE:', e.data);
          }

          // Reset retry count on successful connection
          retryCount.current = 0;
        }
      };

      return es;
    };

    // Initialize event source connection
    const es = connect();
    // Clean up event source on component unmount
    return () => {
      es?.close();
      Object.values(OpenSourceEventTypes).forEach((eventType) => resetEventHandler(eventType));
      clearStatusMessage();
    };
  }, [proxyID]);

  return null;
};
