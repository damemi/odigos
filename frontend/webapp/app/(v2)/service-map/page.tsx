'use client';

import React, { useCallback } from 'react';
import { ROUTES } from '@/utils';
import { useRouter } from 'next/navigation';
import { ServiceMapNext } from '@odigos/ui-kit/containers';

// Polling lives inside `<ServiceMap>` via `useOdigosApi()`.
export default function Page() {
  const router = useRouter();

  // Fired by the Source Drawer's "open in Sampling page" button, as on the overview.
  const onRedirectToSampling = useCallback(() => router.push(ROUTES.SAMPLING), [router]);

  // Shadcn version of the service map (swap back to `ServiceMap` to compare).
  return <ServiceMapNext onRedirectToSampling={onRedirectToSampling} />;
}
