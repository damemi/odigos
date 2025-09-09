import { useEffect, useRef } from 'react';
import { API } from '@/utils';
import { useStatusStore } from '@/store';
import { useDestinationCRUD, useSourceCRUD } from '..';
import { DISPLAY_TITLES } from '@odigos/ui-kit/constants';
import { EntityTypes, StatusType, WorkloadId } from '@odigos/ui-kit/types';
import { getIdFromSseTarget, safeJsonParse } from '@odigos/ui-kit/functions';
import { NotifyPayload, useInstrumentStore, useNotificationStore, usePendingStore } from '@odigos/ui-kit/store';

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

export const useSSE = () => {
  const { setPendingItems } = usePendingStore();
  const { addNotification } = useNotificationStore();
  const { title, setStatusStore } = useStatusStore();
  const { fetchDestinations } = useDestinationCRUD();
  const { fetchSources, fetchSourceById } = useSourceCRUD();

  const retryCount = useRef(0);
  const maxRetries = 10;

  useEffect(() => {
    const connect = () => {
      // If the current status in store is API Token related, we don't want to override it with the connected message
      if (title !== DISPLAY_TITLES.API_TOKEN) {
        setStatusStore({
          status: StatusType.Default,
          title: 'Connecting',
          message: 'Connecting to the server',
        });
      }

      const es = new EventSource(API.SSE_EVENTS);

      es.onopen = () => {
        // If the current status in store is API Token related, we don't want to override it with the connected message
        if (title !== DISPLAY_TITLES.API_TOKEN) {
          setStatusStore({
            status: StatusType.Success,
            title: 'Connected',
            message: 'Connected to the server',
          });
        }
      };

      es.onerror = () => {
        es.close();

        if (retryCount.current < maxRetries) {
          retryCount.current += 1;
          setStatusStore({
            status: StatusType.Warning,
            title: 'Disconnected',
            message: `Disconnected from the server. Retrying connection (${retryCount.current})`,
          });

          // Retry connection with exponential backoff if below max retries
          setTimeout(() => connect(), Math.min(10000, 1000 * Math.pow(2, retryCount.current)));
        } else {
          setStatusStore({
            status: StatusType.Error,
            title: `Connection lost on ${new Date().toLocaleString()}`,
            message: 'Please reboot the application',
          });
          addNotification({
            type: StatusType.Error,
            title: 'Connection Error',
            message: 'Connection to the server failed. Please reboot the application.',
          });
        }
      };

      es.onmessage = (e: MessageEvent) => {
        const data = safeJsonParse<SseEvent | null>(e.data, null);
        const { type, data: sseData } = data || {};
        const isK8sClusterEvent = type && [EventTypes.K8S_CLUSTER_EVENT].includes(type);

        if (isK8sClusterEvent) {
          const notification: NotifyPayload = {
            type: typeof sseData === 'object' ? sseData?.type : StatusType.Info,
            title: typeof sseData === 'object' ? sseData?.event || '' : '',
            message: typeof sseData === 'object' ? sseData?.data || '' : sseData,
            crdType: typeof sseData === 'object' ? sseData?.crdType || '' : '',
            target: typeof sseData === 'object' ? sseData?.target || '' : '',
          };

          const { setInstrumentAwait, isAwaitingInstrumentation, setInstrumentCount, sourcesToCreate, sourcesCreated, sourcesToDelete, sourcesDeleted } = useInstrumentStore.getState();

          const isSource = type === EventTypes.K8S_CLUSTER_EVENT && [OpenSourceCrdTypes.InstrumentationConfig].includes(notification.crdType);
          const isDestination = type === EventTypes.K8S_CLUSTER_EVENT && [OpenSourceCrdTypes.Destination].includes(notification.crdType);

          // do not notify for: connected, modified events, or sources that are still being instrumented
          if ((notification.title || notification.message) && isK8sClusterEvent && !(isSource && isAwaitingInstrumentation) && notification.title !== OpenSourceEventTypes.MODIFIED) {
            addNotification(notification);
          }

          // Handle specific CRD types
          if (isSource) {
            switch (notification.title) {
              case OpenSourceEventTypes.MODIFIED:
                if (!isAwaitingInstrumentation && notification.target) {
                  const id = getIdFromSseTarget(notification.target, EntityTypes.Source);
                  fetchSourceById(id as WorkloadId);
                }
                break;

              case OpenSourceEventTypes.ADDED:
                const created = sourcesCreated + Number(notification.message?.toString().replace(/[^\d]/g, '') || 0);
                setInstrumentCount('sourcesCreated', created);

                // If not waiting, or we're at 100%, then proceed
                if (!isAwaitingInstrumentation || (isAwaitingInstrumentation && created >= sourcesToCreate)) {
                  addNotification({ type: StatusType.Success, title: OpenSourceEventTypes.ADDED, message: `Successfully created ${created} sources` });
                  setInstrumentAwait(false);
                  setInstrumentCount('sourcesToCreate', 0);
                  setInstrumentCount('sourcesCreated', 0);
                  fetchSources();
                }
                break;

              case OpenSourceEventTypes.DELETED:
                const deleted = sourcesDeleted + Number(notification.message?.toString().replace(/[^\d]/g, '') || 0);
                setInstrumentCount('sourcesDeleted', deleted);

                // If not waiting, or we're at 100%, then proceed
                if (!isAwaitingInstrumentation || (isAwaitingInstrumentation && deleted >= sourcesToDelete)) {
                  addNotification({ type: StatusType.Success, title: OpenSourceEventTypes.DELETED, message: `Successfully deleted ${deleted} sources` });
                  setInstrumentAwait(false);
                  setInstrumentCount('sourcesToDelete', 0);
                  setInstrumentCount('sourcesDeleted', 0);
                  fetchSources();
                }
                break;

              default:
                break;
            }
          } else if (isDestination) {
            fetchDestinations();
          } else {
            console.warn('Unhandled SSE:', e.data);
          }

          // This works for now,
          // but in the future we might have to change this to "removePendingItems",
          // and remove the specific pending items based on their entityType and entityId
          setPendingItems([]);

          // Reset retry count on successful connection
          retryCount.current = 0;
        }
      };

      return es;
    };

    // Initialize event source connection
    const es = connect();
    // Clean up event source on component unmount
    return () => es.close();
  }, []);

  return null;
};
