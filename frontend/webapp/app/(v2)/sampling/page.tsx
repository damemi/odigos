'use client';

import React from 'react';
import { ROUTES } from '@/utils';
import { useRouter } from 'next/navigation';
import { SamplingRulesNext } from '@odigos/ui-kit/containers';

// Sampling rules + k8s health probes config + workload list now flow via
// `useOdigosApi()` inside the kit container.
export default function Page() {
  const router = useRouter();

  // PLAT-1605 spike: shadcn version of the sampling page (swap back to `SamplingRules` to compare).
  // "Open in Settings" (tail sampling off) lands on Settings searched to the field that turns it on.
  return <SamplingRulesNext onOpenSettings={(search) => router.push(`${ROUTES.SETTINGS}?${new URLSearchParams({ search })}`)} />;
}
