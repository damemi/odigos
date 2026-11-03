'use client';

import React, { useCallback } from 'react';
import { ROUTES } from '@/utils';
import { useRouter } from 'next/navigation';
import { OverviewNext } from '@odigos/ui-kit/containers';

// All data fetching (metrics polling, effective config for the Profiling
// tab, drawer-fed entity refreshes) lives inside the kit's `<Overview>`
// container via `useOdigosApi()`. The page is a one-liner.
export default function Page() {
  const router = useRouter();

  // Fired by the Source Drawer's "open in Sampling page" hover button.
  // The kit handles both the source-drawer close and staging which rule to
  // open (via `useSamplingDrawerStore`); the page's only job is to navigate.
  const onRedirectToSampling = useCallback(() => router.push(ROUTES.SAMPLING), [router]);
  // URL templatization actions are added and edited on their own page.
  const onRedirectToUrlTemplatization = useCallback(() => router.push(ROUTES.URL_TEMPLATIZATION), [router]);
  const onRedirectToProfiling = useCallback((sourceKey: string | null, unavailableSourceKey?: string) => {
    const params = new URLSearchParams({ ...(sourceKey ? { source: sourceKey } : {}), ...(unavailableSourceKey ? { unavailableSource: unavailableSourceKey } : {}) });
    router.push(`${ROUTES.PROFILING}?${params}`);
  }, [router]);

  // PLAT-1537 spike: shadcn version of the overview (swap back to `Overview` to compare).
  return <OverviewNext onRedirectToProfiling={onRedirectToProfiling} onRedirectToSampling={onRedirectToSampling} onRedirectToUrlTemplatization={onRedirectToUrlTemplatization} />;
}
