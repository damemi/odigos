import { useEffect, useMemo, useRef, useState } from 'react';
import { safeJsonParse } from '@odigos/ui-kit/functions';

export type SseEvent<T = unknown> = {
  type: string;
  data: T;
  cluster?: string;
  timestamp?: number;
};

type Options = {
  url: string;
  withCredentials?: boolean;
  onEvent?: (ev: SseEvent) => void;
};

export function useSse<T = unknown>({ url, withCredentials, onEvent }: Options) {
  const [connected, setConnected] = useState(false);
  const [lastEvent, setLastEvent] = useState<SseEvent<T> | null>(null);
  const esRef = useRef<EventSource | null>(null);

  const eventSourceInit = useMemo(() => ({ withCredentials: !!withCredentials }), [withCredentials]);

  useEffect(() => {
    const es = new EventSource(url, eventSourceInit as EventSourceInit);
    esRef.current = es;

    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
    es.onmessage = (e: MessageEvent) => {
      const parsed = safeJsonParse<SseEvent<T> | null>(e.data, null);
      if (parsed) {
        setLastEvent(parsed);
        onEvent?.(parsed);
      }
    };
    return () => {
      setConnected(false);
      es.close();
    };
  }, [url, eventSourceInit, onEvent]);

  return { connected, lastEvent };
}
