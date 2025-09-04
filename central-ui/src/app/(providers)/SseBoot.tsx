'use client';
import { useCallback } from 'react';
import { API } from '@/utils';
import { useSse, type SseEvent } from '@/hooks/useSse';

export default function SseBoot() {
  const onEvent = useCallback((ev: SseEvent) => {
    //TODO: Replace with a store/notification hook as needed
    // eslint-disable-next-line no-console
    console.log('SSE event', ev);
  }, []);

  useSse({
    url: API.SSE_EVENTS,
    onEvent,
  });

  return null;
}
